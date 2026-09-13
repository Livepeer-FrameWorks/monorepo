package control

import "context"

// ResolveSourceIdentity fills a routing-only entry's public identity through
// Commodore. Background telemetry uses this; media admission uses SourceSnapshot
// and never waits for this identity lookup.
func (r *StreamRegistry) ResolveSourceIdentity(ctx context.Context, tenantID, internalName string) (StreamEntry, error) {
	entry, found, err := r.SourceSnapshot(ctx, tenantID, internalName)
	if err != nil {
		return StreamEntry{}, err
	}
	if !found {
		return StreamEntry{}, ErrUnknownStream
	}
	if entry.StreamID != "" {
		return entry, nil
	}
	entry, err = r.hydrate(ctx, "internal_name", "", "", internalName)
	if err != nil {
		return StreamEntry{}, err
	}
	if entry.TenantID != tenantID || entry.InternalName != internalName {
		return StreamEntry{}, ErrReplicationConflict
	}
	if entry.StreamID == "" {
		return StreamEntry{}, ErrRegistryUnavailable
	}
	return entry, nil
}

// SourceSnapshot returns tenant-scoped current runtime evidence without hydration
// or a control-plane lookup. A configured shared store is authoritative: missing
// or unavailable state cannot fall back to this replica's cached publisher.
func (r *StreamRegistry) SourceSnapshot(ctx context.Context, tenantID, internalName string) (StreamEntry, bool, error) {
	if r == nil || tenantID == "" || internalName == "" {
		return StreamEntry{}, false, ErrReplicationConflict
	}
	e, found, err := r.currentSourceEntry(ctx, internalName)
	if err != nil || !found {
		return StreamEntry{}, false, err
	}
	if e.TenantID != tenantID {
		return StreamEntry{}, false, ErrReplicationConflict
	}
	return StreamEntry{StreamID: e.StreamID, TenantID: e.TenantID, PlaybackID: e.PlaybackID,
		InternalName: e.InternalName, IngestMode: e.IngestMode, RuntimeName: e.RuntimeName,
		OriginClusterID: e.OriginClusterID, Locations: e.Locations}, true, nil
}

func (r *StreamRegistry) currentSourceEntry(ctx context.Context, internalName string) (StreamEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return StreamEntry{}, false, err
	}
	if r == nil || internalName == "" {
		return StreamEntry{}, false, ErrReplicationConflict
	}
	r.mu.RLock()
	store := r.redisStore
	var entry StreamEntry
	var found bool
	if store == nil {
		if cached := r.byInt[internalName]; cached != nil {
			entry = cached.entry
			entry.Locations = cloneLocations(entry.Locations)
			found = true
		}
	}
	r.mu.RUnlock()
	if store != nil {
		var err error
		entry, found, err = store.GetSource(ctx, internalName)
		if err != nil {
			return StreamEntry{}, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return StreamEntry{}, false, err
	}
	if !found {
		return StreamEntry{}, false, nil
	}
	if entry.InternalName != internalName {
		return StreamEntry{}, false, ErrReplicationConflict
	}
	return entry, true, nil
}
