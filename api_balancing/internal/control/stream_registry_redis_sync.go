package control

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// EnableRedisSync wires the registry to a Redis store: rehydrates from
// Redis on startup, write-through on every mutation, and subscribes to
// cross-instance changes published by peer Foghorn instances. Returns the
// number of source + artifact entries rehydrated.
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

	sources, artifacts = r.rehydrateFromRedis(store, logger)

	r.redisWg.Add(1)
	go func() {
		defer r.redisWg.Done()
		subErr := store.Subscribe(subCtx, func(change RegistryChange) {
			if change.InstanceID == instanceID {
				return
			}
			r.applyRedisChange(change)
		})
		if subErr != nil && logger != nil {
			logger.WithError(subErr).Warn("Stream-registry Redis subscription stopped")
		}
	}()

	return sources, artifacts, nil
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

// maxLocationUpdatedAt returns the latest UpdatedAt across every Location on
// the entry. Used as the tombstone-ordering stamp for applyRedisChange's
// stale-delete guard.
func maxLocationUpdatedAt(e StreamEntry) time.Time {
	var t time.Time
	for _, loc := range e.Locations {
		if loc.UpdatedAt.After(t) {
			t = loc.UpdatedAt
		}
	}
	return t
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

func (r *StreamRegistry) applyRedisChange(change RegistryChange) {
	switch change.Entity {
	case RegistryEntitySource:
		if change.Operation == RegistryOpDelete {
			r.mu.Lock()
			// Tombstone ordering: a StreamEntry is per-cluster state, and a
			// stale peer delete must not wipe an entry that was re-admitted
			// locally after the delete was published. Drop the delete when any
			// local Location is fresher than the delete's publish stamp.
			if existing, ok := r.byInt[change.Key]; ok && change.PublishedAtUnixNano > 0 {
				if local := maxLocationUpdatedAt(existing.entry); !local.IsZero() && local.UnixNano() > change.PublishedAtUnixNano {
					r.mu.Unlock()
					if r.redisLogger != nil {
						r.redisLogger.WithFields(map[string]any{
							"internal_name": change.Key,
							"delete_ts":     change.PublishedAtUnixNano,
							"local_ts":      local.UnixNano(),
						}).Debug("applyRedisChange: dropping stale source delete")
					}
					return
				}
			}
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
			// Stale-ordering guard, symmetric to the upsert below: drop a delete
			// published before the entry we currently hold was last cached, so an
			// out-of-order pubsub delete can't wipe a fresher local re-upsert.
			if existing, ok := r.artifacts.byHash[change.Key]; ok &&
				change.PublishedAtUnixNano > 0 && change.PublishedAtUnixNano < existing.cached.UnixNano() {
				r.artifacts.mu.Unlock()
				return
			}
			r.removeArtifactByKeyLocked(change.Key)
			r.artifacts.mu.Unlock()
			return
		}
		var e ArtifactEntry
		if err := json.Unmarshal(change.Payload, &e); err != nil {
			return
		}
		r.artifacts.mu.Lock()
		// Stale-snapshot guard: when a local artifact mutation races a
		// peer's older snapshot still draining the pubsub queue, refuse
		// to overwrite a fresher local entry. Mirrors the source-side
		// guard in this same function; uses PublishedAtUnixNano on the
		// change (ArtifactEntry has no per-mutation field of its own,
		// and HydratedAt is first-hydration, not last-update).
		if existing, ok := r.artifacts.byHash[e.ArtifactHash]; ok && e.ArtifactHash != "" && change.PublishedAtUnixNano > 0 {
			localTS := existing.cached.UnixNano()
			if change.PublishedAtUnixNano < localTS {
				r.artifacts.mu.Unlock()
				if r.redisLogger != nil {
					r.redisLogger.WithFields(map[string]any{
						"artifact_hash": e.ArtifactHash,
						"incoming_ts":   change.PublishedAtUnixNano,
						"local_ts":      localTS,
					}).Debug("applyRedisChange: dropping stale artifact pubsub snapshot")
				}
				return
			}
		}
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
// pubsub change key (the internal_name). Caller holds r.mu.
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

// publishUpsertSource fires-and-forgets a pubsub event to peers. Caller
// must NOT hold r.mu. Logs failures via the logger registered on the
// store; pubsub failures don't fail the write because the source-of-truth
// (Commodore / SQL / federation ad) will re-populate on next refresh.
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
	if err := store.Publish(RegistryChange{
		InstanceID: instance,
		Entity:     RegistryEntitySource,
		Operation:  RegistryOpUpsert,
		Key:        e.InternalName,
		Payload:    payload,
	}); err != nil && log != nil {
		log.WithError(err).WithField("internal_name", e.InternalName).Debug("Stream-registry pubsub source upsert failed")
	}
}

func (r *StreamRegistry) publishDeleteSource(internalName string) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil || internalName == "" {
		return
	}
	if err := store.DeleteSource(internalName); err != nil && log != nil {
		log.WithError(err).WithField("internal_name", internalName).Warn("Stream-registry Redis DeleteSource failed")
	}
	if err := store.Publish(RegistryChange{
		InstanceID:          instance,
		Entity:              RegistryEntitySource,
		Operation:           RegistryOpDelete,
		Key:                 internalName,
		PublishedAtUnixNano: time.Now().UnixNano(),
	}); err != nil && log != nil {
		log.WithError(err).WithField("internal_name", internalName).Debug("Stream-registry pubsub source delete failed")
	}
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
	if err := store.Publish(RegistryChange{
		InstanceID:          instance,
		Entity:              RegistryEntityArtifact,
		Operation:           RegistryOpUpsert,
		Key:                 e.ArtifactHash,
		Payload:             payload,
		PublishedAtUnixNano: time.Now().UnixNano(),
	}); err != nil && log != nil {
		log.WithError(err).WithField("artifact_hash", e.ArtifactHash).Debug("Stream-registry pubsub artifact upsert failed")
	}
}

func (r *StreamRegistry) publishDeleteArtifact(hash string) {
	r.mu.RLock()
	store, instance, log := r.redisStore, r.instanceID, r.redisLogger
	r.mu.RUnlock()
	if store == nil || hash == "" {
		return
	}
	if err := store.DeleteArtifact(hash); err != nil && log != nil {
		log.WithError(err).WithField("artifact_hash", hash).Warn("Stream-registry Redis DeleteArtifact failed")
	}
	if err := store.Publish(RegistryChange{
		InstanceID:          instance,
		Entity:              RegistryEntityArtifact,
		Operation:           RegistryOpDelete,
		Key:                 hash,
		PublishedAtUnixNano: time.Now().UnixNano(),
	}); err != nil && log != nil {
		log.WithError(err).WithField("artifact_hash", hash).Debug("Stream-registry pubsub artifact delete failed")
	}
}
