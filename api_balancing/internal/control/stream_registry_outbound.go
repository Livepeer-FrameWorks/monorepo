package control

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxOutboundDestinations = 4096

// RecordOutboundPull durably records source-side acceptance before a peer is
// acknowledged. Retries for the same destination/source retain the attempt;
// a new explicit attempt fences completion of the previous handoff.
func (r *StreamRegistry) RecordOutboundPull(ctx context.Context, internalName string, pull OutboundPull) (OutboundPull, error) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" || pull.TenantID == "" || pull.DestClusterID == "" || pull.DestNodeID == "" || pull.SourceNodeID == "" || pull.DTSCURL == "" {
		return OutboundPull{}, errors.New("outbound pull requires tenant, stream, source and exact destination")
	}
	if (pull.SourceGeneration != "") != (pull.SourceRevision > 0) || pull.SourceRevision < 0 {
		return OutboundPull{}, ErrReplicationConflict
	}
	now := time.Now()
	requestedAttempt := pull.AttemptID
	if pull.AttemptID == "" {
		pull.AttemptID = uuid.NewString()
	}
	if pull.CreatedAt.IsZero() {
		pull.CreatedAt = now
	}
	pull.UpdatedAt = now
	var recorded OutboundPull
	err := r.mutateOutbound(ctx, internalName, func(entry *StreamEntry, loc *Location) error {
		if entry.TenantID != "" && entry.TenantID != pull.TenantID {
			return ErrReplicationConflict
		}
		if pull.SourceGeneration != "" && (!loc.SourceActive || loc.SourceGeneration != pull.SourceGeneration || loc.SourceRevision != pull.SourceRevision || loc.OwnerNodeID != pull.SourceNodeID) {
			return ErrReplicationConflict
		}
		entry.TenantID = pull.TenantID
		recorded = pull
		for i, current := range loc.OutboundPullers {
			if current.DestClusterID != pull.DestClusterID || current.DestNodeID != pull.DestNodeID {
				continue
			}
			sameSource := current.SourceNodeID == pull.SourceNodeID && current.SourceGeneration == pull.SourceGeneration && current.SourceRevision == pull.SourceRevision && current.DTSCURL == pull.DTSCURL
			if current.SourceMediaClusterID != pull.SourceMediaClusterID {
				if (current.SourceMediaClusterID != "" && pull.SourceMediaClusterID == "") ||
					(sameSource && current.SourceMediaClusterID != "") {
					return ErrReplicationConflict
				}
			}
			if (current.TenantID != "" && current.TenantID != pull.TenantID) || (requestedAttempt != "" && requestedAttempt == current.AttemptID && !sameSource) {
				return ErrReplicationConflict
			}
			if sameSource && (requestedAttempt == "" || requestedAttempt == current.AttemptID) {
				recorded.CreatedAt = current.CreatedAt
				if current.AttemptID != "" {
					recorded.AttemptID = current.AttemptID
				}
			}
			loc.OutboundPullers[i] = recorded
			return nil
		}
		if len(loc.OutboundPullers) >= maxOutboundDestinations {
			return errors.New("outbound destination bound reached")
		}
		loc.OutboundPullers = append(loc.OutboundPullers, recorded)
		return nil
	})
	if err != nil {
		return OutboundPull{}, err
	}
	return recorded, nil
}

// ClearOutboundPull compares the observed attempt against the durable set.
// cutoff, when nonzero, additionally fences expiry against a concurrent renewal.
func (r *StreamRegistry) ClearOutboundPull(ctx context.Context, internalName, destClusterID, destNodeID, attemptID string, cutoff time.Time) error {
	if (attemptID == "" && cutoff.IsZero()) || destClusterID == "" || destNodeID == "" {
		return ErrReplicationConflict
	}
	return r.mutateOutbound(ctx, sourceInternalKey(internalName), func(_ *StreamEntry, loc *Location) error {
		for i, pull := range loc.OutboundPullers {
			if pull.DestClusterID != destClusterID || pull.DestNodeID != destNodeID {
				continue
			}
			if pull.AttemptID != attemptID || (!cutoff.IsZero() && !outboundSeenAt(pull).Before(cutoff)) {
				return ErrReplicationConflict
			}
			loc.OutboundPullers = slices.Delete(loc.OutboundPullers, i, i+1)
			return nil
		}
		return ErrReplicationConflict
	})
}

func outboundSeenAt(pull OutboundPull) time.Time {
	if !pull.UpdatedAt.IsZero() {
		return pull.UpdatedAt
	}
	return pull.CreatedAt
}

func updateOutboundEntry(entry StreamEntry, localCluster string, update func(*StreamEntry, *Location) error) (StreamEntry, error) {
	entry.Locations = maps.Clone(entry.Locations)
	if entry.Locations == nil {
		entry.Locations = make(map[string]Location)
	}
	loc := entry.Locations[localCluster]
	if loc.OutboundRevision == ^uint64(0) {
		return StreamEntry{}, errors.New("outbound revision exhausted")
	}
	loc.OutboundPullers = slices.Clone(loc.OutboundPullers)
	if err := update(&entry, &loc); err != nil {
		return StreamEntry{}, err
	}
	loc.ClusterID, loc.UpdatedAt = localCluster, time.Now()
	loc.OutboundRevision++
	slices.SortFunc(loc.OutboundPullers, func(a, b OutboundPull) int {
		if n := strings.Compare(a.DestClusterID, b.DestClusterID); n != 0 {
			return n
		}
		return strings.Compare(a.DestNodeID, b.DestNodeID)
	})
	entry.Locations[localCluster] = loc
	return entry, nil
}

func (r *StreamRegistry) mutateOutbound(ctx context.Context, internalName string, update func(*StreamEntry, *Location) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if internalName == "" {
		return ErrReplicationConflict
	}
	r.mu.Lock()
	store, instance := r.redisStore, r.instanceID
	if store == nil {
		entry := StreamEntry{InternalName: internalName, HydratedAt: time.Now()}
		if ce := r.byInt[internalName]; ce != nil {
			entry = ce.entry
		}
		entry, err := updateOutboundEntry(entry, r.clusterID, update)
		if err == nil {
			r.installReplicationEntryLocked(entry)
		}
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	entry, err := store.mutateOutbound(ctx, internalName, instance, update)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if ce := r.byInt[internalName]; ce != nil {
		entry = mergeStreamEntry(ce.entry, entry)
	}
	r.installReplicationEntryLocked(entry)
	r.mu.Unlock()
	return nil
}

func (r *RedisRegistryStore) mutateOutbound(ctx context.Context, internalName, instance string, update func(*StreamEntry, *Location) error) (StreamEntry, error) {
	for range 16 {
		entry, raw, err := r.readSourceSnapshot(ctx, internalName)
		if err != nil {
			return StreamEntry{}, err
		}
		entry, err = updateOutboundEntry(entry, r.clusterID, update)
		if err != nil {
			return StreamEntry{}, err
		}
		revision := sourceRevisionForCluster(entry, r.clusterID)
		change := RegistryChange{InstanceID: instance, Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: internalName, SourceRevision: revision}
		result, err := r.compareAndSetSource(ctx, entry, change, revision, raw)
		if err != nil {
			return StreamEntry{}, err
		}
		if result == 1 {
			return entry, nil
		}
		if result == 0 {
			return StreamEntry{}, ErrReplicationConflict
		}
	}
	return StreamEntry{}, errors.New("outbound mutation contention")
}

func preserveOutbound(current, incoming Location) Location {
	if current.OutboundRevision > 0 && current.OutboundRevision >= incoming.OutboundRevision {
		incoming.OutboundRevision = current.OutboundRevision
		incoming.OutboundPullers = slices.Clone(current.OutboundPullers)
	}
	return incoming
}
