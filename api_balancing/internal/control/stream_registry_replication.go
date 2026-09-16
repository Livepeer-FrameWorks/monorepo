package control

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxInboundDestinations = 4096

var ErrReplicationConflict = errors.New("replication attempt superseded")

func cloneLocations(locations map[string]Location) map[string]Location {
	result := make(map[string]Location, len(locations))
	for key, loc := range locations {
		loc.InboundPulls = maps.Clone(loc.InboundPulls)
		loc.SourceNodes = slices.Clone(loc.SourceNodes)
		loc.EdgeCandidates = slices.Clone(loc.EdgeCandidates)
		loc.OutboundPullers = slices.Clone(loc.OutboundPullers)
		result[key] = loc
	}
	return result
}

// InboundPull identifies one source connection into one destination node. Revision
// orders its mutations independently of the publisher's source-ownership revision.
type InboundPull struct {
	TenantID  string
	AttemptID string
	Revision  uint64
	Cleared   bool
	// DestinationObserved is a live-state notification marker, not source revocation or generation-bound readiness.
	DestinationObserved  bool
	SourceClusterID      string // Source control cell; distinct from its virtual media cluster.
	SourceMediaClusterID string
	SourceNodeID         string
	SourceGeneration     string
	SourceRevision       int64
	DestClusterID        string
	DestNodeID           string
	DestNodeBaseURL      string
	DTSCURL              string
	CreatedAt            time.Time
	SourceAcceptedAt     time.Time
	// PlacementDemand retains request context for a fresh policy evaluation;
	// it is not admission evidence and does not extend any receipt lifetime.
	PlacementDemand string
	// PlacementRequired is a fail-closed marker, never admission evidence.
	PlacementRequired bool
}

// RecordInboundPlacementDemand binds non-authorizing request context to the
// existing physical attempt. Clearing or replacing the pull invalidates the write.
func (r *StreamRegistry) RecordInboundPlacementDemand(ctx context.Context, internalName string, expected InboundPull, demand string) error {
	if demand == "" || len(demand) > 65536 {
		return ErrReplicationConflict
	}
	return r.bindInboundPlacement(ctx, internalName, expected, demand)
}

// RequireInboundPlacement fences a reused physical pull before its URL is exposed.
func (r *StreamRegistry) RequireInboundPlacement(ctx context.Context, internalName string, expected InboundPull) error {
	if expected.DTSCURL == "" {
		return ErrReplicationConflict
	}
	return r.bindInboundPlacement(ctx, internalName, expected, "")
}

func (r *StreamRegistry) bindInboundPlacement(ctx context.Context, internalName string, expected InboundPull, demand string) error {
	if expected.TenantID == "" || expected.AttemptID == "" || expected.DestClusterID == "" || expected.DestNodeID == "" {
		return ErrReplicationConflict
	}
	_, err := r.mutateInboundPull(ctx, sourceInternalKey(internalName), expected.DestNodeID, func(current InboundPull, exists bool) (InboundPull, error) {
		if !exists || current.Cleared || current.TenantID != expected.TenantID || current.AttemptID != expected.AttemptID ||
			current.DestClusterID != expected.DestClusterID || current.DestNodeID != expected.DestNodeID ||
			current.SourceClusterID != expected.SourceClusterID || current.SourceMediaClusterID != expected.SourceMediaClusterID ||
			current.SourceNodeID != expected.SourceNodeID || current.SourceGeneration != expected.SourceGeneration || current.SourceRevision != expected.SourceRevision ||
			(expected.DTSCURL != "" && current.DTSCURL != expected.DTSCURL) {
			return InboundPull{}, ErrReplicationConflict
		}
		current.PlacementRequired = true
		if demand != "" {
			current.PlacementDemand = demand
		}
		return current, nil
	})
	return err
}

func inboundPulls(loc Location) map[string]InboundPull {
	pulls := maps.Clone(loc.InboundPulls)
	if pulls == nil {
		pulls = make(map[string]InboundPull)
	}
	return pulls
}

func mergeInboundPulls(a, b Location) map[string]InboundPull {
	merged := inboundPulls(a)
	for node, incoming := range inboundPulls(b) {
		current, exists := merged[node]
		if !exists || incoming.Revision > current.Revision || (incoming.Revision == current.Revision && incoming.AttemptID == current.AttemptID && incoming.Cleared) {
			merged[node] = incoming
		}
	}
	return merged
}

func activeInboundPulls(loc Location) []InboundPull {
	var out []InboundPull
	for _, pull := range inboundPulls(loc) {
		if !pull.Cleared && pull.SourceClusterID != "" && pull.DestNodeID != "" && pull.DTSCURL != "" {
			out = append(out, pull)
		}
	}
	slices.SortFunc(out, func(a, b InboundPull) int { return strings.Compare(a.DestNodeID, b.DestNodeID) })
	return out
}

func replicationView(loc Location, p InboundPull) Location {
	loc.ReplicatingFrom, loc.PullDTSCURL = p.SourceClusterID, p.DTSCURL
	loc.DestNodeID, loc.DestNodeBaseURL = p.DestNodeID, p.DestNodeBaseURL
	loc.PullSourceNodeID = p.SourceNodeID
	loc.InboundPulls = maps.Clone(loc.InboundPulls)
	loc.SourceNodes = slices.Clone(loc.SourceNodes)
	loc.EdgeCandidates = slices.Clone(loc.EdgeCandidates)
	loc.OutboundPullers = slices.Clone(loc.OutboundPullers)
	return loc
}

// syncReplicationView keeps the singleton diagnostic view deterministic. Routing
// to a particular node must use LocalReplicationForNode, never this representative.
func syncReplicationView(loc Location) Location {
	loc.ReplicatingFrom, loc.PullDTSCURL, loc.DestNodeID = "", "", ""
	loc.DestNodeBaseURL, loc.PullSourceNodeID = "", ""
	pulls := activeInboundPulls(loc)
	if len(pulls) > 0 {
		loc = replicationView(loc, pulls[0])
	}
	return loc
}

// RecordInboundPull records the exact accepted destination. Persistence failures
// are returned before the caller advertises that destination or starts its pull.
func (r *StreamRegistry) RecordInboundPull(ctx context.Context, internalName string, pull InboundPull) (InboundPull, error) {
	return r.recordInboundPull(ctx, internalName, pull, false)
}

// RecordInboundConfiguredPull permits a configured source with unchanged
// configuration authority to move to another origin node. The caller must have
// re-evaluated current configured-source placement and supplied a new attempt;
// push publishers continue through RecordInboundPull and cannot use this path.
func (r *StreamRegistry) RecordInboundConfiguredPull(ctx context.Context, internalName string, pull InboundPull) (InboundPull, error) {
	return r.recordInboundPull(ctx, internalName, pull, true)
}

func (r *StreamRegistry) recordInboundPull(ctx context.Context, internalName string, pull InboundPull, allowEquivalentSourceReplacement bool) (InboundPull, error) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" || pull.DestNodeID == "" || pull.SourceClusterID == "" || pull.DTSCURL == "" {
		return InboundPull{}, fmt.Errorf("replication requires stream, source, destination and DTSC URL")
	}
	if pull.SourceMediaClusterID != "" && (pull.TenantID == "" || pull.DestClusterID == "" || pull.SourceNodeID == "") {
		return InboundPull{}, ErrReplicationConflict
	}
	if (pull.SourceGeneration != "") != (pull.SourceRevision > 0) || pull.SourceRevision < 0 {
		return InboundPull{}, ErrReplicationConflict
	}
	if pull.AttemptID == "" {
		pull.AttemptID = uuid.NewString()
	}
	if pull.CreatedAt.IsZero() {
		pull.CreatedAt = time.Now()
	}
	pull.DestinationObserved = false
	pull.PlacementDemand = ""
	return r.mutateInboundPull(ctx, internalName, pull.DestNodeID, func(current InboundPull, exists bool) (InboundPull, error) {
		if exists {
			if (current.TenantID != "" && current.TenantID != pull.TenantID) || (current.DestClusterID != "" && current.DestClusterID != pull.DestClusterID) {
				return InboundPull{}, ErrReplicationConflict
			}
			// Revocation retains the source fence. A delayed acceptance cannot
			// restore an older generation or reopen the cleared attempt.
			if current.SourceClusterID == pull.SourceClusterID && (pull.SourceRevision < current.SourceRevision ||
				(pull.SourceRevision == current.SourceRevision && current.SourceRevision > 0 && pull.SourceGeneration != current.SourceGeneration)) {
				return InboundPull{}, ErrReplicationConflict
			}
			if current.Cleared && current.AttemptID == pull.AttemptID {
				return InboundPull{}, ErrReplicationConflict
			}
		}
		if exists && !current.Cleared {
			if current.SourceClusterID != pull.SourceClusterID {
				return InboundPull{}, ErrReplicationConflict
			}
			if current.SourceMediaClusterID != "" && pull.SourceMediaClusterID == "" {
				return InboundPull{}, ErrReplicationConflict
			}
			if pull.SourceRevision > current.SourceRevision && pull.AttemptID != current.AttemptID {
				return pull, nil
			}
			if allowEquivalentSourceReplacement && pull.AttemptID != current.AttemptID &&
				pull.SourceGeneration == current.SourceGeneration && pull.SourceRevision == current.SourceRevision &&
				(pull.SourceMediaClusterID != current.SourceMediaClusterID || pull.SourceNodeID != current.SourceNodeID ||
					SourcePullBaseURL(pull.DTSCURL) != SourcePullBaseURL(current.DTSCURL)) {
				return pull, nil
			}
			// The DTSC comparison is on the media path only. The stored URL
			// carries this attempt's source credential, which a renewal reissues
			// and a secret rotation changes outright; comparing the credentialed
			// form would wedge an otherwise identical pull as a conflict.
			if current.SourceNodeID != pull.SourceNodeID || current.SourceGeneration != pull.SourceGeneration || current.SourceRevision != pull.SourceRevision ||
				SourcePullBaseURL(current.DTSCURL) != SourcePullBaseURL(pull.DTSCURL) {
				return InboundPull{}, ErrReplicationConflict
			}
			if current.SourceMediaClusterID != pull.SourceMediaClusterID {
				// Revalidation may fill an absent binding on the same physical
				// attempt; it cannot move that attempt to another virtual cluster.
				if current.SourceMediaClusterID != "" || current.AttemptID != pull.AttemptID {
					return InboundPull{}, ErrReplicationConflict
				}
				current.SourceMediaClusterID = pull.SourceMediaClusterID
			}
			if pull.SourceMediaClusterID != "" && (current.TenantID == "" || current.DestClusterID == "") {
				if current.AttemptID != pull.AttemptID {
					return InboundPull{}, ErrReplicationConflict
				}
				current.TenantID, current.DestClusterID = pull.TenantID, pull.DestClusterID
			}
			current.PlacementRequired = current.PlacementRequired || pull.PlacementRequired
			if current.AttemptID == pull.AttemptID && pull.SourceAcceptedAt.After(current.SourceAcceptedAt) {
				current.SourceAcceptedAt = pull.SourceAcceptedAt
			}
			return current, nil
		}
		return pull, nil
	})
}

// MarkInboundDestinationObserved records the live-state notification without
// clearing the source binding needed by warm preparation and source reconnects.
// This is not proof that the observed media belongs to this source generation.
func (r *StreamRegistry) MarkInboundDestinationObserved(ctx context.Context, internalName, nodeID, attemptID string) (bool, error) {
	if attemptID == "" || nodeID == "" {
		return false, ErrReplicationConflict
	}
	_, err := r.mutateInboundPull(ctx, sourceInternalKey(internalName), nodeID, func(current InboundPull, exists bool) (InboundPull, error) {
		if !exists || current.Cleared || current.DestinationObserved || current.AttemptID != attemptID {
			return InboundPull{}, ErrReplicationConflict
		}
		current.DestinationObserved = true
		return current, nil
	})
	return err == nil, err
}

// ClearInboundPull is fenced to the observed attempt. A delayed revocation or
// node disappearance from an earlier pull cannot remove its replacement.
func (r *StreamRegistry) ClearInboundPull(ctx context.Context, internalName, nodeID, attemptID string) (bool, error) {
	if attemptID == "" || nodeID == "" {
		return false, ErrReplicationConflict
	}
	_, err := r.mutateInboundPull(ctx, sourceInternalKey(internalName), nodeID, func(current InboundPull, exists bool) (InboundPull, error) {
		if !exists || current.Cleared || current.AttemptID != attemptID {
			return InboundPull{}, ErrReplicationConflict
		}
		current.Cleared = true
		current.PlacementDemand = ""
		return current, nil
	})
	return err == nil, err
}

func updateInboundEntry(entry StreamEntry, localCluster, nodeID string, revision uint64, update func(InboundPull, bool) (InboundPull, error)) (StreamEntry, InboundPull, error) {
	entry.Locations = maps.Clone(entry.Locations)
	if entry.Locations == nil {
		entry.Locations = make(map[string]Location)
	}
	loc := entry.Locations[localCluster]
	loc.ClusterID = localCluster
	pulls := inboundPulls(loc)
	current, exists := pulls[nodeID]
	if !exists && len(pulls) >= maxInboundDestinations {
		return entry, InboundPull{}, fmt.Errorf("replication destination limit reached")
	}
	pull, err := update(current, exists)
	if err != nil {
		return entry, InboundPull{}, err
	}
	if entry.TenantID != "" && pull.TenantID != "" && entry.TenantID != pull.TenantID {
		return entry, InboundPull{}, ErrReplicationConflict
	}
	if entry.TenantID == "" {
		entry.TenantID = pull.TenantID
	}
	pull.Revision = revision
	pulls[nodeID] = pull
	loc.InboundPulls = pulls
	loc = syncReplicationView(loc)
	loc.UpdatedAt = time.Now()
	entry.Locations[localCluster] = loc
	return entry, pull, nil
}

func (r *StreamRegistry) mutateInboundPull(ctx context.Context, internalName, nodeID string, update func(InboundPull, bool) (InboundPull, error)) (InboundPull, error) {
	if internalName == "" || nodeID == "" {
		return InboundPull{}, ErrReplicationConflict
	}
	r.mu.Lock()
	store, instance := r.redisStore, r.instanceID
	if store == nil {
		entry := StreamEntry{InternalName: internalName, HydratedAt: time.Now()}
		if ce := r.byInt[internalName]; ce != nil {
			entry = ce.entry
		}
		var revision uint64
		for _, p := range inboundPulls(entry.Locations[r.clusterID]) {
			revision = max(revision, p.Revision)
		}
		if revision == ^uint64(0) {
			r.mu.Unlock()
			return InboundPull{}, fmt.Errorf("replication revision exhausted")
		}
		entry, pull, err := updateInboundEntry(entry, r.clusterID, nodeID, revision+1, update)
		if err == nil {
			r.installReplicationEntryLocked(entry)
		}
		r.mu.Unlock()
		return pull, err
	}
	r.mu.Unlock()
	entry, pull, err := store.MutateInboundPull(ctx, internalName, nodeID, instance, update)
	if err != nil {
		return InboundPull{}, err
	}
	r.mu.Lock()
	if ce := r.byInt[internalName]; ce != nil {
		entry = mergeStreamEntry(ce.entry, entry)
	}
	r.installReplicationEntryLocked(entry)
	r.mu.Unlock()
	return pull, nil
}

func (r *StreamRegistry) installReplicationEntryLocked(entry StreamEntry) {
	ce := &cachedEntry{entry: entry, cached: time.Now()}
	r.byInt[entry.InternalName] = ce
	if entry.StreamID != "" {
		r.byID[entry.StreamID] = ce
	}
	if entry.PlaybackID != "" {
		r.byPlay[entry.PlaybackID] = ce
	}
}

func (r *StreamRegistry) InboundPullForNode(internalName, nodeID string) (InboundPull, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce := r.byInt[sourceInternalKey(internalName)]
	if ce == nil || nodeID == "" {
		return InboundPull{}, false
	}
	pull, ok := inboundPulls(ce.entry.Locations[r.clusterID])[nodeID]
	return pull, ok && !pull.Cleared
}

// CurrentInboundPull reads shared state when configured. A missing or unavailable
// durable record cannot fall back to this replica's possibly stale pull cache.
func (r *StreamRegistry) CurrentInboundPull(ctx context.Context, internalName, nodeID string) (InboundPull, bool, error) {
	if nodeID == "" {
		return InboundPull{}, false, ErrReplicationConflict
	}
	pulls, err := r.CurrentInboundPulls(ctx, internalName)
	if err != nil {
		return InboundPull{}, false, err
	}
	pull, found := pulls[nodeID]
	return pull, found && !pull.Cleared, nil
}

// CurrentInboundPulls includes clearing tombstones and other destinations so a
// source resolver can distinguish unarranged media from an explicitly fenced pull.
func (r *StreamRegistry) CurrentInboundPulls(ctx context.Context, internalName string) (map[string]InboundPull, error) {
	location, err := r.CurrentSourceLocation(ctx, internalName)
	return location.InboundPulls, err
}

// CurrentSourceLocation reads publisher ownership and destination pulls together
// so a source resolver cannot mix a new publisher claim with an old pull fence.
func (r *StreamRegistry) CurrentSourceLocation(ctx context.Context, internalName string) (Location, error) {
	internalName = sourceInternalKey(internalName)
	entry, found, err := r.currentSourceEntry(ctx, internalName)
	if err != nil {
		return Location{}, err
	}
	if !found {
		return Location{}, nil
	}
	location := entry.Locations[r.clusterID]
	pulls := inboundPulls(location)
	if len(pulls) > maxInboundDestinations {
		return Location{}, ErrReplicationConflict
	}
	for nodeID, pull := range pulls {
		if nodeID == "" || pull.DestNodeID != nodeID {
			return Location{}, ErrReplicationConflict
		}
	}
	location.InboundPulls = pulls
	return location, nil
}

func (r *StreamRegistry) LocalReplicationForNode(_ context.Context, internalName, nodeID string) (Location, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce := r.byInt[sourceInternalKey(internalName)]
	if ce == nil || nodeID == "" {
		return Location{}, false
	}
	loc := ce.entry.Locations[r.clusterID]
	pull, ok := inboundPulls(loc)[nodeID]
	if !ok || pull.Cleared || pull.DTSCURL == "" {
		return Location{}, false
	}
	return replicationView(loc, pull), true
}
