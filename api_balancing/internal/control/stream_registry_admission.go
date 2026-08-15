package control

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RuntimeNameForStream returns the registry-resolved Mist runtime name
// for a source stream identified by its internal name. Returns the bare
// internal name when no entry is hydrated yet — admission-time callers
// pass freshly-validated streams that may not have hit the registry yet,
// and a replaced-source drain target must still have a usable runtime name
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

// ProjectSource publishes the DB-confirmed publisher generation cluster-wide. The caller supplies a
// positive revision allocated while holding the stream advisory lock; lower revisions mutate nothing.
// priorOwnerNodeID identifies a different projected node that the caller must drain. Pull ownership
// uses MarkSourceOwnerIfUnset instead of this versioned push-source transition.
func (r *StreamRegistry) ProjectSource(internalName, nodeID string, connectorPID int64, triggerUUID, generation string, revision int64) (priorOwnerNodeID string, applied bool, err error) {
	priorOwnerNodeID, _, applied, err = r.projectSourceWithPriorGeneration(internalName, nodeID, connectorPID, triggerUUID, generation, revision)
	return priorOwnerNodeID, applied, err
}

func (r *StreamRegistry) projectSourceWithPriorGeneration(internalName, nodeID string, connectorPID int64, triggerUUID, generation string, revision int64) (priorOwnerNodeID, priorOwnerSourceGeneration string, applied bool, err error) {
	internalName = strings.TrimSpace(internalName)
	nodeID = strings.TrimSpace(nodeID)
	if internalName == "" || nodeID == "" {
		return "", "", false, nil
	}
	if revision <= 0 {
		return "", "", false, errors.New("project source requires a positive revision")
	}
	var snapshot StreamEntry
	var before Location
	var beforePresent bool
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
	before, beforePresent = loc, loc.ClusterID != "" || loc.OwnerNodeID != "" || loc.SourceRevision != 0
	triggerUUID = strings.TrimSpace(triggerUUID)
	generation = strings.TrimSpace(generation)
	if loc.SourceRevision > revision {
		r.mu.Unlock()
		return "", "", false, nil
	}
	// Equal revisions are idempotent only for the exact same transition. A revision is allocated
	// once under the stream lock and cannot legitimately describe another publisher identity.
	if loc.SourceRevision == revision && revision != 0 && (!loc.SourceActive ||
		loc.OwnerNodeID != nodeID || loc.SourceConnectorPID != connectorPID ||
		loc.SourceTriggerUUID != triggerUUID || loc.SourceGeneration != generation) {
		r.mu.Unlock()
		return "", "", false, nil
	}
	if prior := strings.TrimSpace(loc.OwnerNodeID); prior != "" && prior != nodeID {
		priorOwnerNodeID = prior
		priorOwnerSourceGeneration = strings.TrimSpace(loc.SourceGeneration)
	}
	loc.ClusterID = r.clusterID
	loc.SourceActive = true
	loc.SourceInactiveAt = time.Time{}
	loc.OwnerNodeID = nodeID
	loc.SourceConnectorPID = connectorPID
	loc.SourceTriggerUUID = triggerUUID
	loc.SourceGeneration = generation
	loc.SourceRevision = revision
	loc.UpdatedAt = time.Now()
	ce.entry.Locations[r.clusterID] = loc
	ce.cached = time.Now()
	snapshot = ce.entry
	r.mu.Unlock()
	applied, err = r.publishUpsertSourceFenced(snapshot)
	if err == nil && applied {
		return priorOwnerNodeID, priorOwnerSourceGeneration, true, nil
	}
	// The Redis CAS is the shared authority. Undo only this exact local revision; a concurrent newer
	// transition must remain intact.
	r.mu.Lock()
	if current, ok := r.byInt[internalName]; ok {
		locNow := current.entry.Locations[r.clusterID]
		if locNow.SourceRevision == revision && locNow.SourceGeneration == generation {
			if beforePresent {
				current.entry.Locations[r.clusterID] = before
			} else {
				delete(current.entry.Locations, r.clusterID)
			}
		}
	}
	r.mu.Unlock()
	if err != nil {
		return "", "", false, err
	}
	return "", "", false, nil
}

// MarkSourceOwnerIfUnset atomically stamps nodeID as the stream's source
// owner iff no owner is currently recorded on the local cluster's
// Location. First dialer wins: an existing owner (same or different node)
// is returned untouched, so a later /source call — a relay, a probe, a
// double-dial — can never flip ownership. This is the ownership stamp for
// pull sources, the counterpart of ProjectSource's owner stamp for push
// ingest; offlineIsStreamWide consumes it to type offline edges.
//
// A missing entry is created minimally (same pattern as ProjectSource and
// MarkReplicating; no network under the lock, resolvers refine identity
// later). Callers only invoke this after positively
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

// SourceOwner returns the local cluster's recorded source-owner node for a stream. Only the owner's
// STREAM_END or vanish is stream-wide; a replica or relay ending is node-local. Inactive source
// projections retain the owner so a delayed aggregate edge is still typed correctly.
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

// PublishSourceInactive upserts an INACTIVE source Location stamped with `revision` and publishes it,
// CREATING the entry if this replica is cache-cold (has no local source). It never no-ops on a
// missing/already-inactive entry — the point is to PROPAGATE a genuine offline (its
// higher revision supersedes any active projection another replica/Redis still holds) rather than
// silently do nothing. The caller MUST have confirmed under the (tenant, stream) advisory lock that no
// active session exists, so there is no live source to protect. The ending node IS the owner, so it is
// stamped as OwnerNodeID (retained for resume typing) and its generation preserved. Never regresses the
// revision.
func (r *StreamRegistry) PublishSourceInactive(internalName, nodeID, generation string, revision int64) (bool, error) {
	return r.PublishSourceInactiveContext(context.Background(), internalName, nodeID, generation, revision)
}

// PublishSourceInactiveContext is PublishSourceInactive with caller-owned cancellation. Durable
// offline effects use it while holding the admission advisory lock, so Redis persistence cannot
// outlive the transaction context that protects teardown from a reconnect.
func (r *StreamRegistry) PublishSourceInactiveContext(ctx context.Context, internalName, nodeID, generation string, revision int64) (bool, error) {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return false, nil
	}
	if revision <= 0 {
		return false, errors.New("publish source inactive requires a positive revision")
	}
	nodeID = strings.TrimSpace(nodeID)
	generation = strings.TrimSpace(generation)
	var snapshot StreamEntry
	var before Location
	var beforePresent bool
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
	before, beforePresent = loc, loc.ClusterID != "" || loc.OwnerNodeID != "" || loc.SourceRevision != 0
	if loc.SourceRevision > revision {
		r.mu.Unlock()
		return false, nil
	}
	if loc.SourceRevision == revision && revision != 0 && (loc.SourceActive ||
		(nodeID != "" && loc.OwnerNodeID != "" && loc.OwnerNodeID != nodeID) ||
		(generation != "" && loc.SourceGeneration != "" && loc.SourceGeneration != generation)) {
		r.mu.Unlock()
		return false, nil
	}
	loc.ClusterID = r.clusterID
	loc.SourceActive = false
	if loc.SourceInactiveAt.IsZero() {
		loc.SourceInactiveAt = time.Now()
	}
	if strings.TrimSpace(loc.OwnerNodeID) == "" && nodeID != "" {
		loc.OwnerNodeID = nodeID
	}
	if strings.TrimSpace(loc.SourceGeneration) == "" && generation != "" {
		loc.SourceGeneration = generation
	}
	loc.SourceRevision = revision
	loc.UpdatedAt = time.Now()
	ce.entry.Locations[r.clusterID] = loc
	ce.cached = time.Now()
	snapshot = ce.entry
	r.mu.Unlock()
	applied, err := r.publishUpsertSourceFencedContext(ctx, snapshot)
	if err == nil && applied {
		return true, nil
	}
	// Redis owns the cross-replica revision order. Undo only this exact local transition when a
	// higher revision already won or persistence failed; a concurrent newer transition remains.
	r.mu.Lock()
	if current, ok := r.byInt[internalName]; ok {
		locNow := current.entry.Locations[r.clusterID]
		if locNow.SourceRevision == revision && !locNow.SourceActive {
			if beforePresent {
				current.entry.Locations[r.clusterID] = before
			} else {
				delete(current.entry.Locations, r.clusterID)
			}
		}
	}
	r.mu.Unlock()
	if err != nil {
		return false, err
	}
	return false, nil
}

// SourceGenerationSnapshot returns the current source's DURABLE generation (the ingest-session id) and
// active flag for (internalName, nodeID), and ok=false when there is no matching active-or-recorded
// source on this node. Offline intents retain this generation as the transition they supersede.
func (r *StreamRegistry) SourceGenerationSnapshot(internalName, nodeID string) (generation string, active bool, ok bool) {
	internalName = strings.TrimSpace(internalName)
	nodeID = strings.TrimSpace(nodeID)
	if internalName == "" {
		return "", false, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce, present := r.byInt[internalName]
	if !present {
		return "", false, false
	}
	loc, has := ce.entry.Locations[r.clusterID]
	if !has || strings.TrimSpace(loc.OwnerNodeID) == "" {
		return "", false, false
	}
	if nodeID != "" && loc.OwnerNodeID != nodeID {
		return "", false, false
	}
	return loc.SourceGeneration, loc.SourceActive, true
}
