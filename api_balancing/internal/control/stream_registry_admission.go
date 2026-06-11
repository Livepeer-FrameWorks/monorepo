package control

import (
	"strings"
	"time"
)

// RuntimeNameForStream returns the registry-resolved Mist runtime name
// for a source stream identified by its internal name. Returns the bare
// internal name when no entry is hydrated yet — admission-time callers
// pass freshly-validated streams that may not have hit the registry yet,
// and the takeover drain target should fall back to a sensible literal
// rather than fail closed.
func RuntimeNameForStream(r *StreamRegistry, internalName string) string {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" || r == nil {
		return internalName
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ce, ok := r.byInt[internalName]; ok && ce.entry.RuntimeName != "" {
		return ce.entry.RuntimeName
	}
	return internalName
}

// AdmissionDecision is the outcome of AdmitAndReserve.
type AdmissionDecision int

const (
	// AdmissionRejectDuplicate: another publisher is actively pushing this
	// stream (source_active=true) and the new attempt is not a provable
	// same-session reconnect. Mist response is empty (denies the push).
	AdmissionRejectDuplicate AdmissionDecision = iota + 1
	// AdmissionAcceptNew: no prior owner is recorded for this stream on
	// this cluster. Normal first-time admission.
	AdmissionAcceptNew
	// AdmissionAcceptResume: source is inactive and the candidate is the
	// recorded owner node. Mist's resume path can reuse the lingering
	// buffer.
	AdmissionAcceptResume
	// AdmissionAcceptTakeover: source is inactive and the candidate is a
	// different node from the recorded owner. Old owner must be drained
	// before viewers race to the new node.
	AdmissionAcceptTakeover
)

func (d AdmissionDecision) String() string {
	switch d {
	case AdmissionRejectDuplicate:
		return "reject_duplicate"
	case AdmissionAcceptNew:
		return "accept_new"
	case AdmissionAcceptResume:
		return "accept_resume"
	case AdmissionAcceptTakeover:
		return "accept_takeover"
	default:
		return "unknown"
	}
}

// AdmissionResult bundles the decision plus any context the caller needs
// to act on it. OldOwnerNodeID is populated only for AdmissionAcceptTakeover.
type AdmissionResult struct {
	Decision       AdmissionDecision
	OldOwnerNodeID string
}

// AdmitAndReserve is the source-presence admission decision for a new
// PUSH_REWRITE on this cluster, combining the decision read and the
// source-active flip under a single write lock. Atomicity prevents
// two concurrent same-stream PUSH_REWRITEs from both reading
// SourceActive=false and both accepting.
//
// Decision table:
//   - source_active=true (any node): reject as duplicate. Same-session
//     reconnect detection is deferred — without a publisher conn-id
//     marker, the conservative default is to reject; the legitimate
//     case (Mist-internal retry) will succeed once PUSH_INPUT_CLOSE
//     fires.
//   - source_active=false, same node as recorded owner: accept_resume.
//     Mist's resume path matches unclaimed tracks against the lingering
//     buffer.
//   - source_active=false, different node from recorded owner:
//     accept_takeover. Caller must drain the old owner before admitting
//     viewers to the new node.
//   - no recorded owner: accept_new. First publisher for this stream.
//
// ownerHealthy is an optional callback the caller can supply to short-
// circuit "source_active=true but the owner node is stale/down" into
// the source_active=false branch. Nil means "trust the recorded flag".
// It runs UNDER r.mu, establishing the lock order registry.mu → state.mu
// (the production callback reads StreamStateManager under its RLock).
// Safe because state never calls into control; keep the callback free of
// registry methods and other I/O.
//
// On any Accept variant, atomically sets SourceActive=true +
// OwnerNodeID=candidateNodeID + clears SourceInactiveAt. On Reject,
// no mutation. Creates a minimal StreamEntry if none exists for the
// AcceptNew case; the caller's UpsertLocalSource later refines
// identity fields. Callers must NOT also call MarkSourceActive — the
// flip is part of the same critical section as the decision.
func (r *StreamRegistry) AdmitAndReserve(internalName, candidateNodeID string, ownerHealthy func(nodeID string) bool) AdmissionResult {
	internalName = strings.TrimSpace(internalName)
	candidateNodeID = strings.TrimSpace(candidateNodeID)
	if internalName == "" || candidateNodeID == "" {
		return AdmissionResult{Decision: AdmissionAcceptNew}
	}

	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	var loc Location
	var locPresent bool
	if ok {
		loc, locPresent = ce.entry.Locations[r.clusterID]
	}

	var result AdmissionResult
	if !locPresent || loc.OwnerNodeID == "" {
		result = AdmissionResult{Decision: AdmissionAcceptNew}
	} else {
		sourceActive := loc.SourceActive
		if sourceActive && ownerHealthy != nil && !ownerHealthy(loc.OwnerNodeID) {
			sourceActive = false
		}
		switch {
		case sourceActive:
			r.mu.Unlock()
			return AdmissionResult{Decision: AdmissionRejectDuplicate}
		case loc.OwnerNodeID == candidateNodeID:
			result = AdmissionResult{Decision: AdmissionAcceptResume}
		default:
			result = AdmissionResult{Decision: AdmissionAcceptTakeover, OldOwnerNodeID: loc.OwnerNodeID}
		}
	}

	// Atomic reservation: flip source-active + owner in the same lock
	// scope as the decision read.
	if ce == nil {
		ce = &cachedEntry{
			entry: StreamEntry{
				InternalName: internalName,
				Locations:    make(map[string]Location),
				HydratedAt:   time.Now(),
			},
			cached: time.Now(),
		}
		r.byInt[internalName] = ce
	}
	if ce.entry.Locations == nil {
		ce.entry.Locations = make(map[string]Location)
	}
	loc = ce.entry.Locations[r.clusterID]
	loc.ClusterID = r.clusterID
	loc.SourceActive = true
	loc.SourceInactiveAt = time.Time{}
	loc.OwnerNodeID = candidateNodeID
	loc.UpdatedAt = time.Now()
	ce.entry.Locations[r.clusterID] = loc
	ce.cached = time.Now()
	snapshot := ce.entry
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
	return result
}

// MarkSourceActive flips the local cluster's Location to
// SourceActive=true and stamps OwnerNodeID. Called from the PUSH_REWRITE
// path immediately after admission accepts the new publisher. Idempotent —
// re-marking the same owner just refreshes UpdatedAt; marking a different
// owner overwrites OwnerNodeID (takeover already drained the previous
// owner before this call lands).
//
// Hydration: callers must ensure the StreamEntry exists in the registry
// before calling. The PUSH_REWRITE admission path does so via the existing
// hydration in ResolveSourceByInternalName, so this method only mutates
// already-known entries.
func (r *StreamRegistry) MarkSourceActive(internalName, nodeID string) {
	internalName = strings.TrimSpace(internalName)
	nodeID = strings.TrimSpace(nodeID)
	if internalName == "" || nodeID == "" {
		return
	}
	var snapshot StreamEntry
	var changed bool
	r.mu.Lock()
	if ce, ok := r.byInt[internalName]; ok {
		if ce.entry.Locations == nil {
			ce.entry.Locations = make(map[string]Location)
		}
		loc := ce.entry.Locations[r.clusterID]
		loc.ClusterID = r.clusterID
		loc.SourceActive = true
		loc.SourceInactiveAt = time.Time{}
		loc.OwnerNodeID = nodeID
		loc.UpdatedAt = time.Now()
		ce.entry.Locations[r.clusterID] = loc
		ce.cached = time.Now()
		snapshot = ce.entry
		changed = true
	}
	r.mu.Unlock()
	if changed {
		r.publishUpsertSource(snapshot)
	}
}

// MarkSourceInactive flips SourceActive to false on the local cluster's
// Location and stamps SourceInactiveAt. Retains OwnerNodeID so a same-node
// PUSH_REWRITE reconnect can take the Mist resume path. Called from the
// PUSH_INPUT_CLOSE handler (the publisher-source-disconnected edge).
// Idempotent — repeat calls just refresh the timestamp.
//
// If nodeID is supplied and does not match the recorded OwnerNodeID, the
// call is a no-op: PUSH_INPUT_CLOSE on a stale/wrong node must not clear
// the live owner's state. Empty nodeID skips the match check (defensive
// for older trigger payloads).
func (r *StreamRegistry) MarkSourceInactive(internalName, nodeID string) {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	var snapshot StreamEntry
	var changed bool
	r.mu.Lock()
	if ce, ok := r.byInt[internalName]; ok {
		if loc, present := ce.entry.Locations[r.clusterID]; present {
			if nodeID != "" && loc.OwnerNodeID != "" && loc.OwnerNodeID != nodeID {
				r.mu.Unlock()
				return
			}
			if loc.SourceActive || loc.SourceInactiveAt.IsZero() {
				loc.SourceActive = false
				loc.SourceInactiveAt = time.Now()
				loc.UpdatedAt = time.Now()
				ce.entry.Locations[r.clusterID] = loc
				ce.cached = time.Now()
				snapshot = ce.entry
				changed = true
			}
		}
	}
	r.mu.Unlock()
	if changed {
		r.publishUpsertSource(snapshot)
	}
}
