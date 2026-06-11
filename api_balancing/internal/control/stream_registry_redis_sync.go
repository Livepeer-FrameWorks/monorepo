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
	artifacts, err := store.GetAllArtifacts()
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to rehydrate artifact entries from Redis")
		}
	}

	r.mu.Lock()
	for _, e := range sources {
		ce := &cachedEntry{entry: e, cached: time.Now()}
		if e.StreamID != "" {
			r.byID[e.StreamID] = ce
		}
		if e.InternalName != "" {
			r.byInt[e.InternalName] = ce
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
		// Take incoming when the cluster is new locally or incoming is
		// newer-or-equal; keep the strictly-newer local Location otherwise.
		if !ok || !inLoc.UpdatedAt.Before(cur.UpdatedAt) {
			locs[cid] = inLoc
		}
	}
	merged.Locations = locs
	return merged
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
			r.removeSourceByKeyLocked(change.Key)
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
func (r *StreamRegistry) publishUpsertSource(e StreamEntry) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil {
		return
	}
	if err := store.SetSource(e); err != nil {
		if log != nil {
			log.WithError(err).WithField("internal_name", e.InternalName).Warn("Stream-registry Redis SetSource failed")
		}
		return
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	r.publishChange(store, log, RegistryChange{
		InstanceID: instance,
		Entity:     RegistryEntitySource,
		Operation:  RegistryOpUpsert,
		Key:        e.InternalName,
		Payload:    payload,
	})
}

func (r *StreamRegistry) publishDeleteSource(internalName string) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil || internalName == "" {
		return
	}
	if err := store.DeleteSource(internalName); err != nil {
		if log != nil {
			log.WithError(err).WithField("internal_name", internalName).Warn("Stream-registry Redis DeleteSource failed; retrying in background")
		}
		retryRegistryDeleteAsync(log, "source", internalName, func() error { return store.DeleteSource(internalName) })
	}
	r.publishChange(store, log, RegistryChange{
		InstanceID: instance,
		Entity:     RegistryEntitySource,
		Operation:  RegistryOpDelete,
		Key:        internalName,
	})
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
