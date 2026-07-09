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
// identity fields. Callers must NOT also stamp ownership separately
// (MarkSourceOwnerIfUnset is for pull sources) — the flip is part of
// the same critical section as the decision.
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

// MarkSourceOwnerIfUnset atomically stamps nodeID as the stream's source
// owner iff no owner is currently recorded on the local cluster's
// Location. First dialer wins: an existing owner (same or different node)
// is returned untouched, so a later /source call — a relay, a probe, a
// double-dial — can never flip ownership. This is the ownership stamp for
// pull sources, the counterpart of AdmitAndReserve's inline stamp for
// push ingest; offlineIsStreamWide consumes it to type offline edges.
//
// A missing entry is created minimally (same pattern as AdmitAndReserve's
// AcceptNew and MarkReplicating; no network under the lock, resolvers
// refine identity later). Callers only invoke this after positively
// resolving the stream (the /source pull path has just confirmed an
// enabled pull source with Commodore), so the stamp must not silently
// degrade to backstop-only offline just because the local cache lacked
// the entry.
func (r *StreamRegistry) MarkSourceOwnerIfUnset(internalName, nodeID string) (string, bool) {
	internalName = sourceInternalKey(internalName)
	nodeID = strings.TrimSpace(nodeID)
	if internalName == "" || nodeID == "" {
		return "", false
	}
	var snapshot StreamEntry
	var stamped bool
	var owner string
	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	if !ok {
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
	loc := ce.entry.Locations[r.clusterID]
	if loc.OwnerNodeID != "" {
		owner = loc.OwnerNodeID
	} else {
		loc.ClusterID = r.clusterID
		loc.SourceActive = true
		loc.SourceInactiveAt = time.Time{}
		loc.OwnerNodeID = nodeID
		loc.UpdatedAt = time.Now()
		ce.entry.Locations[r.clusterID] = loc
		ce.cached = time.Now()
		snapshot = ce.entry
		owner = nodeID
		stamped = true
	}
	r.mu.Unlock()
	if stamped {
		r.publishUpsertSource(snapshot)
	}
	return owner, stamped
}

// SourceOwner returns the local cluster's recorded source-owner node for a
// stream. Ownership types offline edges: only the owner's STREAM_END or
// vanish is a stream-wide fact — a replica/relay node ending the stream is
// a node-local fact that must not flip the stream's user-visible status.
// OwnerNodeID survives MarkSourceInactive (retained for the reconnect
// resume path), so the owner is still known when the delayed STREAM_END
// arrives after PUSH_INPUT_CLOSE.
func (r *StreamRegistry) SourceOwner(internalName string) (string, bool) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce, ok := r.byInt[internalName]
	if !ok {
		return "", false
	}
	loc, ok := ce.entry.Locations[r.clusterID]
	if !ok || strings.TrimSpace(loc.OwnerNodeID) == "" {
		return "", false
	}
	return loc.OwnerNodeID, true
}

// OriginCluster returns the stream's origin cluster ID when known.
// Log-only context for offline suppression — the authority decision never
// depends on it (no recorded local owner already means node-local).
func (r *StreamRegistry) OriginCluster(internalName string) (string, bool) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce, ok := r.byInt[internalName]
	if !ok {
		return "", false
	}
	origin := strings.TrimSpace(ce.entry.OriginClusterID)
	if origin == "" {
		return "", false
	}
	return origin, true
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
