package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

// EnableRedisSync wires the registry to a Redis store: rehydrates from
// Redis on startup, write-through on every mutation, and follows the
// ordered, replayable changelog of cross-instance changes. The startup
// sequence is a consistent cut — capture the changelog tail FIRST, then
// load the key snapshot, then replay from the captured tail — so no change
// can fall between snapshot and live sync. Returns the number of source +
// artifact entries rehydrated.
//
// Matches the pattern used by state.StreamStateManager.EnableRedisSync so
// operators see one consistent persistence model across Foghorn caches.
func (r *StreamRegistry) EnableRedisSync(ctx context.Context, store *RedisRegistryStore, instanceID string, logger logging.Logger) (sources, artifacts int, err error) {
	subCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.redisStore = store
	r.instanceID = instanceID
	r.redisCancel = cancel
	r.redisLogger = logger
	r.mu.Unlock()

	tail, tailErr := store.ChangelogTail(subCtx)
	if tailErr != nil {
		if logger != nil {
			logger.WithError(tailErr).Warn("Failed to read registry changelog tail; replaying from start of retained log")
		}
		tail = "0-0"
	}

	sources, artifacts = r.rehydrateFromRedis(store, logger)

	r.redisWg.Add(1)
	go func() {
		defer r.redisWg.Done()
		cursor := tail
		for {
			subErr := store.ReadChanges(subCtx, cursor, r.handleRegistryChangelogEntry)
			if errors.Is(subErr, pkgredis.ErrChangelogGap) && subCtx.Err() == nil {
				// The cursor fell behind the trimmed window (long
				// partition): re-run the consistent cut instead of
				// continuing blind. Re-applying keys is idempotent —
				// entries merge per-Location and watermarks gate.
				if logger != nil {
					logger.Warn("Stream-registry changelog reader fell behind retention; re-running consistent cut")
				}
				newTail, tailErr2 := store.ChangelogTail(subCtx)
				if tailErr2 != nil {
					newTail = "0-0"
				}
				r.rehydrateFromRedis(store, logger)
				cursor = newTail
				continue
			}
			if subErr != nil && logger != nil {
				logger.WithError(subErr).Warn("Stream-registry changelog reader stopped")
			}
			return
		}
	}()

	return sources, artifacts, nil
}

// handleRegistryChangelogEntry applies one changelog entry: self-originated
// entries only advance the watermark (publish already did, but replay after
// a restart lands here), peer entries apply only when newer than the key's
// watermark — so a stale or replayed entry can never roll back a later
// local write, regardless of any instance's wall clock.
func (r *StreamRegistry) handleRegistryChangelogEntry(id string, change RegistryChange) {
	key := string(change.Entity) + "|" + change.Key
	if change.InstanceID == r.instanceID {
		r.watermarks.Record(key, id)
		return
	}
	if !r.watermarks.ShouldApply(key, id) {
		return
	}
	r.applyRedisChange(change)
}

// DisableRedisSync stops the subscription goroutine. Safe to call from
// shutdown handlers.
func (r *StreamRegistry) DisableRedisSync() {
	r.mu.Lock()
	cancel := r.redisCancel
	r.redisCancel = nil
	r.redisStore = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.redisWg.Wait()
}

func (r *StreamRegistry) rehydrateFromRedis(store *RedisRegistryStore, logger logging.Logger) (int, int) {
	sources, err := store.GetAllSources()
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to rehydrate source entries from Redis")
		}
		return 0, 0
	}
	revisions, err := store.GetAllSourceRevisions()
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to rehydrate source revision watermarks from Redis")
		}
		return 0, 0
	}
	artifacts, err := store.GetAllArtifacts()
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to rehydrate artifact entries from Redis")
		}
	}

	r.mu.Lock()
	for internalName, revision := range revisions {
		if _, exists := sources[internalName]; exists {
			continue
		}
		if current, ok := r.byInt[internalName]; ok && sourceRevisionForCluster(current.entry, r.clusterID) <= revision {
			r.removeSourceByKeyLocked(internalName)
		}
	}
	for _, e := range sources {
		if e.InternalName == "" {
			continue
		}
		if revision := revisions[e.InternalName]; revision > sourceRevisionForCluster(e, r.clusterID) {
			continue
		}
		if current, ok := r.byInt[e.InternalName]; ok {
			e = mergeStreamEntry(current.entry, e)
			r.removeSourceByKeyLocked(e.InternalName)
		}
		ce := &cachedEntry{entry: e, cached: time.Now()}
		r.byInt[e.InternalName] = ce
		if e.StreamID != "" {
			r.byID[e.StreamID] = ce
		}
		if e.PlaybackID != "" {
			r.byPlay[e.PlaybackID] = ce
		}
	}
	r.mu.Unlock()

	r.artifacts.mu.Lock()
	for _, e := range artifacts {
		ce := &cachedArtifact{entry: e, cached: time.Now()}
		r.artifacts.byHash[e.ArtifactHash] = ce
		if e.InternalName != "" {
			r.artifacts.byInternal[e.InternalName] = ce
		}
		if e.Kind == ArtifactKindProcessing {
			r.artifacts.byProcessingKey[e.ArtifactHash] = ce
		}
	}
	r.artifacts.mu.Unlock()

	return len(sources), len(artifacts)
}

// mergeStreamEntry merges an incoming peer snapshot into the local view of a
// source. Locations is per-cluster state, so each Location is reconciled
// independently (newest UpdatedAt wins) rather than the whole entry being
// replaced — a snapshot that is fresh for one cluster but stale for another
// must never roll back the fresher cluster's Location. Locations only the
// local side knows are preserved (no per-Location tombstones exist, so a
// snapshot that omits a cluster is treated as "no opinion", not a removal).
// Stable identity fields are taken from the local side, filled from incoming
// only when locally empty.
func mergeStreamEntry(existing, incoming StreamEntry) StreamEntry {
	merged := existing
	if merged.StreamID == "" {
		merged.StreamID = incoming.StreamID
	}
	if merged.TenantID == "" {
		merged.TenantID = incoming.TenantID
	}
	if merged.PlaybackID == "" {
		merged.PlaybackID = incoming.PlaybackID
	}
	if merged.InternalName == "" {
		merged.InternalName = incoming.InternalName
	}
	if merged.IngestMode == 0 {
		merged.IngestMode = incoming.IngestMode
	}
	if merged.RuntimeName == "" {
		merged.RuntimeName = incoming.RuntimeName
	}
	if merged.OriginClusterID == "" {
		merged.OriginClusterID = incoming.OriginClusterID
	}
	if merged.HydratedAt.IsZero() {
		merged.HydratedAt = incoming.HydratedAt
	}
	// Auth identity is filled from a peer only when the local side hasn't
	// hydrated it (same "stable identity from local, fill from incoming when
	// empty" rule). A peer that has resolved the auth bit warms ours so the
	// live resolve carries it without each instance re-hydrating.
	if !merged.RequiresAuthKnown && incoming.RequiresAuthKnown {
		merged.RequiresAuth = incoming.RequiresAuth
		merged.RequiresAuthKnown = true
		merged.ClusterPeers = incoming.ClusterPeers
	}
	if len(incoming.Locations) == 0 {
		return merged
	}
	// Copy the existing map so we never mutate the cached entry in place.
	locs := make(map[string]Location, len(merged.Locations)+len(incoming.Locations))
	for cid, loc := range merged.Locations {
		locs[cid] = loc
	}
	for cid, inLoc := range incoming.Locations {
		cur, ok := locs[cid]
		if !ok {
			locs[cid] = inLoc
			continue
		}
		locs[cid] = mergeLocationRevisioned(cur, inLoc)
	}
	merged.Locations = locs
	return merged
}

// mergeLocationRevisioned merges two views of the same cluster's Location. Non-source location state
// (replication, pull, liveness, and edges) is last-writer-wins by UpdatedAt. The
// source-OWNERSHIP fields are merged SEPARATELY by SourceRevision (highest wins), so an unrelated
// location write from a stale replica (fresh UpdatedAt, but an old source it has not caught up on)
// cannot make stale source ownership look "newer" and clobber the real publisher. Equal revisions
// (including both 0 — a pull-ownership stamp or unversioned) fall back to UpdatedAt.
func mergeLocationRevisioned(cur, incoming Location) Location {
	// Base = the UpdatedAt winner for all the NON-source fields.
	base := cur
	if !incoming.UpdatedAt.Before(cur.UpdatedAt) {
		base = incoming
	}
	// Source-ownership winner: higher revision, else (equal) the UpdatedAt winner.
	src := cur
	switch {
	case incoming.SourceRevision > cur.SourceRevision:
		src = incoming
	case incoming.SourceRevision < cur.SourceRevision:
		src = cur
	default:
		if !incoming.UpdatedAt.Before(cur.UpdatedAt) {
			src = incoming
		}
	}
	base.SourceActive = src.SourceActive
	base.SourceInactiveAt = src.SourceInactiveAt
	base.OwnerNodeID = src.OwnerNodeID
	base.SourceConnectorPID = src.SourceConnectorPID
	base.SourceTriggerUUID = src.SourceTriggerUUID
	base.SourceGeneration = src.SourceGeneration
	base.SourceRevision = src.SourceRevision
	return base
}

// applyRedisChange applies a peer's changelog entry to the local in-memory
// view. Ordering is already settled by the caller (changelog entry IDs +
// per-key watermarks), so there are no staleness guards here: a delete that
// reaches this function is by definition newer than anything local, and a
// stale one was already dropped. Sources still merge per-Location because
// Locations is per-cluster state with its own owner semantics.
func (r *StreamRegistry) applyRedisChange(change RegistryChange) {
	switch change.Entity {
	case RegistryEntitySource:
		if change.Operation == RegistryOpDelete {
			r.mu.Lock()
			if ce, ok := r.byInt[change.Key]; !ok || sourceRevisionForCluster(ce.entry, r.clusterID) <= change.SourceRevision {
				r.removeSourceByKeyLocked(change.Key)
			}
			r.mu.Unlock()
			return
		}
		var e StreamEntry
		if err := json.Unmarshal(change.Payload, &e); err != nil {
			return
		}
		r.mu.Lock()
		// Per-Location merge instead of wholesale replace: Locations is
		// per-cluster state, so a snapshot that is fresh for cluster B but
		// stale for cluster A must not roll back A's SourceActive/owner state.
		// Each Location is merged independently, newest UpdatedAt wins, and
		// Locations only the local side knows are preserved (CRDT-style).
		merged := e
		if existing, ok := r.byInt[e.InternalName]; ok && e.InternalName != "" {
			merged = mergeStreamEntry(existing.entry, e)
		}
		ce := &cachedEntry{entry: merged, cached: time.Now()}
		if merged.StreamID != "" {
			r.byID[merged.StreamID] = ce
		}
		if merged.InternalName != "" {
			r.byInt[merged.InternalName] = ce
		}
		if merged.PlaybackID != "" {
			r.byPlay[merged.PlaybackID] = ce
		}
		r.mu.Unlock()
	case RegistryEntityArtifact:
		if change.Operation == RegistryOpDelete {
			r.artifacts.mu.Lock()
			r.removeArtifactByKeyLocked(change.Key)
			r.artifacts.mu.Unlock()
			return
		}
		var e ArtifactEntry
		if err := json.Unmarshal(change.Payload, &e); err != nil {
			return
		}
		r.artifacts.mu.Lock()
		ce := &cachedArtifact{entry: e, cached: time.Now()}
		r.artifacts.byHash[e.ArtifactHash] = ce
		if e.InternalName != "" {
			r.artifacts.byInternal[e.InternalName] = ce
		}
		if e.Kind == ArtifactKindProcessing {
			r.artifacts.byProcessingKey[e.ArtifactHash] = ce
		}
		r.artifacts.mu.Unlock()
	}
}

// removeSourceByKeyLocked drops every map index for a source given the
// changelog change key (the internal_name). Caller holds r.mu.
func (r *StreamRegistry) removeSourceByKeyLocked(internalName string) {
	if internalName == "" {
		return
	}
	if ce, ok := r.byInt[internalName]; ok {
		if ce.entry.StreamID != "" {
			delete(r.byID, ce.entry.StreamID)
		}
		if ce.entry.PlaybackID != "" {
			delete(r.byPlay, ce.entry.PlaybackID)
		}
	}
	delete(r.byInt, internalName)
}

// removeArtifactByKeyLocked drops indexes for an artifact given its hash.
// Caller holds r.artifacts.mu.
func (r *StreamRegistry) removeArtifactByKeyLocked(hash string) {
	if hash == "" {
		return
	}
	if ce, ok := r.artifacts.byHash[hash]; ok && ce.entry.InternalName != "" {
		delete(r.artifacts.byInternal, ce.entry.InternalName)
	}
	delete(r.artifacts.byHash, hash)
	delete(r.artifacts.byProcessingKey, hash)
}

// publishUpsertSource write-throughs the entry and appends the change to
// the changelog. Caller must NOT hold r.mu. Logs failures via the logger
// registered on the store; changelog failures don't fail the write because
// the source-of-truth (Commodore / SQL / federation ad) will re-populate on
// next refresh.
func sourceRevisionForCluster(e StreamEntry, clusterID string) int64 {
	if loc, ok := e.Locations[clusterID]; ok {
		return loc.SourceRevision
	}
	return 0
}

func (r *StreamRegistry) publishUpsertSourceFenced(e StreamEntry) (bool, error) {
	return r.publishUpsertSourceFencedContext(context.Background(), e)
}

func (r *StreamRegistry) publishUpsertSourceFencedContext(ctx context.Context, e StreamEntry) (bool, error) {
	r.mu.RLock()
	store, instance := r.redisStore, r.instanceID
	r.mu.RUnlock()
	if store == nil {
		return true, nil
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	change := RegistryChange{
		InstanceID:     instance,
		Entity:         RegistryEntitySource,
		Operation:      RegistryOpUpsert,
		Key:            e.InternalName,
		Payload:        payload,
		SourceRevision: sourceRevisionForCluster(e, r.clusterID),
	}
	return store.SetSourceRevisioned(ctx, e, change, change.SourceRevision)
}

func (r *StreamRegistry) publishUpsertSource(e StreamEntry) {
	applied, err := r.publishUpsertSourceFenced(e)
	if err != nil && r.redisLogger != nil {
		r.redisLogger.WithError(err).WithField("internal_name", e.InternalName).Warn("Stream-registry revisioned source publish failed")
		return
	}
	if applied {
		return
	}

	// This is a generic registry mutation, not a source-ownership transition. If this replica is
	// behind, retain its newer non-source fields while taking ownership from Redis's higher revision,
	// then retry. A missing durable value means a newer tombstone won and must not be resurrected.
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		latest, found, readErr := store.GetSource(ctx, e.InternalName)
		if readErr != nil {
			if log != nil {
				log.WithError(readErr).WithField("internal_name", e.InternalName).Warn("Stream-registry source reconcile read failed")
			}
			return
		}
		if !found {
			if log != nil {
				log.WithField("internal_name", e.InternalName).Debug("Stream-registry source mutation superseded by tombstone")
			}
			return
		}
		reconciled := mergeStreamEntry(latest, e)
		payload, marshalErr := json.Marshal(reconciled)
		if marshalErr != nil {
			return
		}
		revision := sourceRevisionForCluster(reconciled, r.clusterID)
		change := RegistryChange{
			InstanceID: instance, Entity: RegistryEntitySource, Operation: RegistryOpUpsert,
			Key: reconciled.InternalName, Payload: payload, SourceRevision: revision,
		}
		retried, retryErr := store.SetSourceRevisioned(ctx, reconciled, change, revision)
		if retryErr != nil {
			if log != nil {
				log.WithError(retryErr).WithField("internal_name", e.InternalName).Warn("Stream-registry reconciled source publish failed")
			}
			return
		}
		if retried {
			r.applyRedisChange(change)
			return
		}
	}
	if log != nil {
		log.WithField("internal_name", e.InternalName).Warn("Stream-registry source mutation remained behind concurrent ownership transitions")
	}
}

// publishDeleteSource publishes the entry's eviction as a revisioned tombstone. revision is the local
// cluster's last-known source revision, captured by the caller BEFORE it cleared the Location (a
// cleared map always reads 0, which a versioned watermark rejects — the delete would silently never
// happen and the durable value would resurrect the entry on the next rehydrate). When the caller has
// no revision (the location was already absent), the durable watermark itself carries the delete:
// equal-revision deletes are accepted (delete-if-not-superseded), so the watermark is exactly the
// newest revision this delete is entitled to erase, and a concurrent newer projection still wins the
// CAS.
// publishDeleteSource returns true when the delete is SETTLED — the tombstone was durably published,
// or it lost the revision CAS to a strictly newer transition (which now owns the key, so there is
// nothing left to retry). It returns false only on a TRANSIENT failure (watermark read or Redis
// script error); the caller must then RETAIN its local entry so a later sweep retries — this is what
// makes the retry real rather than a comment (an evicted entry leaves nothing to retry from).
func (r *StreamRegistry) publishDeleteSource(entry StreamEntry, revision int64) bool {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil || entry.InternalName == "" {
		// No durable store to reconcile against — nothing to publish, nothing to retry.
		return true
	}
	if revision == 0 {
		watermark, wErr := store.GetSourceRevision(context.Background(), entry.InternalName)
		if wErr != nil {
			if log != nil {
				log.WithError(wErr).WithField("internal_name", entry.InternalName).Warn("Stream-registry source delete could not read the revision watermark; entry retained for retry")
			}
			return false
		}
		revision = watermark
	}
	change := RegistryChange{
		InstanceID:     instance,
		Entity:         RegistryEntitySource,
		Operation:      RegistryOpDelete,
		Key:            entry.InternalName,
		SourceRevision: revision,
	}
	applied, err := store.DeleteSourceRevisioned(context.Background(), entry.InternalName, change, revision)
	if err != nil {
		if log != nil {
			log.WithError(err).WithField("internal_name", entry.InternalName).Warn("Stream-registry revisioned source delete failed; entry retained for retry")
		}
		return false
	}
	if !applied && log != nil {
		log.WithField("internal_name", entry.InternalName).Debug("Stream-registry source delete lost the revision CAS to a newer transition")
	}
	return true
}

func (r *StreamRegistry) publishUpsertArtifact(e ArtifactEntry) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.SetArtifact(e); err != nil {
		if log != nil {
			log.WithError(err).WithField("artifact_hash", e.ArtifactHash).Warn("Stream-registry Redis SetArtifact failed")
		}
		return
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	r.publishChange(store, log, RegistryChange{
		InstanceID: instance,
		Entity:     RegistryEntityArtifact,
		Operation:  RegistryOpUpsert,
		Key:        e.ArtifactHash,
		Payload:    payload,
	})
}

func (r *StreamRegistry) publishDeleteArtifact(hash string) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil || hash == "" {
		return
	}
	if err := store.DeleteArtifact(hash); err != nil {
		if log != nil {
			log.WithError(err).WithField("artifact_hash", hash).Warn("Stream-registry Redis DeleteArtifact failed; retrying in background")
		}
		retryRegistryDeleteAsync(log, "artifact", hash, func() error { return store.DeleteArtifact(hash) })
	}
	r.publishChange(store, log, RegistryChange{
		InstanceID: instance,
		Entity:     RegistryEntityArtifact,
		Operation:  RegistryOpDelete,
		Key:        hash,
	})
}

// registryDeleteRetryBackoff paces retryRegistryDeleteAsync. Package var so
// tests can shrink it.
var registryDeleteRetryBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

// retryRegistryDeleteAsync retries a failed write-through key delete off
// the hot path. The changelog delete entry is still appended — live
// replicas converge regardless — so the only exposure is a later restart's
// rehydrate resurrecting the stale key (bounded further by the registry's
// lookup TTL); these retries close that window.
func retryRegistryDeleteAsync(log logging.Logger, kind, key string, del func() error) {
	go func() {
		for _, wait := range registryDeleteRetryBackoff {
			time.Sleep(wait)
			if del() == nil {
				return
			}
		}
		if log != nil {
			log.WithFields(map[string]any{"kind": kind, "key": key}).Error("Stream-registry write-through delete kept failing; stale key may resurrect on a future restart's rehydrate")
		}
	}()
}

// publishChange appends a change to the changelog and records its entry ID
// as the key's watermark, so a peer entry logged before this write can never
// be applied over it afterwards.
func (r *StreamRegistry) publishChange(store *RedisRegistryStore, log logging.Logger, change RegistryChange) {
	id, err := store.Publish(change)
	if err != nil {
		if log != nil {
			log.WithError(err).WithFields(map[string]any{"entity": change.Entity, "key": change.Key}).Debug("Stream-registry changelog append failed")
		}
		return
	}
	r.watermarks.Record(string(change.Entity)+"|"+change.Key, id)
}
