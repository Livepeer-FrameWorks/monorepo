package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// OfflineEffectIntent describes the idempotent stream-offline work that must follow an authoritative
// no-active-session decision. The intent is persisted in the same transaction that draws its source
// revision; no trigger goroutine owns completion.
type OfflineEffectIntent struct {
	SetNodeOffline   bool
	TeardownStream   bool
	BroadcastOffline bool
	DecklogTrigger   []byte
}

// OfflineEffect is one leased durable offline transition.
type OfflineEffect struct {
	ID               int64
	TenantID         string
	InternalName     string
	NodeID           string
	SourceGeneration string
	SourceRevision   int64
	SetNodeOffline   bool
	TeardownStream   bool
	BroadcastOffline bool
	DecklogTrigger   []byte
	LeaseToken       string
}

// ErrOfflineEffectSuperseded means a newer source transition already won the shared revision CAS.
// The durable row is terminal, but none of its external effects may run.
var ErrOfflineEffectSuperseded = errors.New("offline effect superseded by newer source revision")

// ErrOfflineEffectDeferred means the claimant lacks the authority an owed part of this effect needs
// (the federation-leader-owned peer broadcast/untrack). The worker releases the lease without a
// failure penalty; the leader replica applies the effect on its own tick.
var ErrOfflineEffectDeferred = errors.New("offline effect deferred to the authoritative replica")

// ReleaseOfflineEffectNotOwner releases a claimed offline effect without a failure penalty (the
// mirror of ReleaseAdmissionEffectNotOwner).
func ReleaseOfflineEffectNotOwner(ctx context.Context, effect OfflineEffect, authorityInstance string) error {
	if db == nil || effect.ID <= 0 || effect.LeaseToken == "" {
		return nil
	}
	err := foghorndb.New(db).ReleaseOfflineEffectNotOwner(ctx, foghorndb.ReleaseOfflineEffectNotOwnerParams{
		EffectID: effect.ID, LeaseToken: effect.LeaseToken, AuthorityInstance: authorityInstance,
	})
	if err != nil {
		return fmt.Errorf("release offline effect to leader: %w", err)
	}
	return nil
}

func enqueueOfflineEffectTx(ctx context.Context, tx *sql.Tx, tenantID, internalName, nodeID, generation string, revision int64, intent OfflineEffectIntent) error {
	if revision <= 0 {
		return fmt.Errorf("enqueue offline effect requires positive source revision")
	}
	err := foghorndb.New(tx).EnqueueOfflineEffect(ctx, foghorndb.EnqueueOfflineEffectParams{
		TenantID: tenantID, StreamInternalName: internalName, SourceNodeID: nodeID,
		SourceGeneration: generation, SourceRevision: revision, SetNodeOffline: intent.SetNodeOffline,
		TeardownStream: intent.TeardownStream, BroadcastOffline: intent.BroadcastOffline, DecklogTrigger: intent.DecklogTrigger,
	})
	if err != nil {
		return fmt.Errorf("enqueue offline effect: %w", err)
	}
	return nil
}

// ClaimOfflineEffects leases due transitions across HA replicas. A worker that dies loses only its
// lease; the row remains pending and another worker retries every idempotent effect. Cell-scoped
// administrative scan over Foghorn's own schema (the tenant-filter rule's documented exception):
// the outbox drains every tenant's due transitions in one pass, and each claimed row carries its
// tenant_id, which scopes the advisory lock and every effect applied under it.
func ClaimOfflineEffects(ctx context.Context, limit int, lease time.Duration, instanceID string) ([]OfflineEffect, error) {
	if db == nil || limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	rows, err := foghorndb.New(db).ClaimOfflineEffects(ctx, foghorndb.ClaimOfflineEffectsParams{
		InstanceID: sql.NullString{String: instanceID, Valid: instanceID != ""}, LeaseMs: lease.Milliseconds(), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim offline effects: %w", err)
	}
	out := make([]OfflineEffect, 0, len(rows))
	for _, row := range rows {
		out = append(out, OfflineEffect{
			ID: row.ID, TenantID: row.TenantID, InternalName: row.StreamInternalName, NodeID: row.SourceNodeID,
			SourceGeneration: row.SourceGeneration, SourceRevision: row.SourceRevision,
			SetNodeOffline: row.SetNodeOffline, TeardownStream: row.TeardownStream, BroadcastOffline: row.BroadcastOffline,
			DecklogTrigger: row.DecklogTrigger, LeaseToken: row.LeaseToken,
		})
	}
	return out, nil
}

// ApplyClaimedOfflineEffect serializes the final no-active-session check and every external effect
// against admission with the shared stream advisory lock. The callback runs while the lock is held;
// a reconnect therefore either commits first and supersedes the row, or waits until the complete
// idempotent teardown finishes and then re-establishes live state.
func ApplyClaimedOfflineEffect(ctx context.Context, effect OfflineEffect, apply func(context.Context, OfflineEffect) error) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("apply offline effect: no database configured")
	}
	if effect.ID <= 0 || effect.TenantID == "" || effect.InternalName == "" || effect.LeaseToken == "" {
		return false, fmt.Errorf("apply offline effect missing identity")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin offline effect tx: %w", err)
	}
	defer rollbackQuiet(tx)
	qtx := foghorndb.New(tx)
	if lockErr := qtx.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
		return false, fmt.Errorf("lock offline effect stream: %w", lockErr)
	}
	_, err = qtx.LockOfflineEffectLease(ctx, foghorndb.LockOfflineEffectLeaseParams{EffectID: effect.ID, LeaseToken: effect.LeaseToken})
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("lock offline effect lease: %w", err)
	}
	active, probeErr := qtx.HasActiveIngestSession(ctx, foghorndb.HasActiveIngestSessionParams{TenantID: effect.TenantID, StreamInternalName: effect.InternalName})
	if probeErr != nil {
		return false, fmt.Errorf("recheck offline effect authority: %w", probeErr)
	}
	if active {
		n, updateErr := qtx.SupersedeOfflineEffect(ctx, foghorndb.SupersedeOfflineEffectParams{EffectID: effect.ID, LeaseToken: effect.LeaseToken})
		if updateErr != nil {
			return false, fmt.Errorf("supersede offline effect: %w", updateErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit superseded offline effect: %w", commitErr)
		}
		return n == 1, nil
	}
	if apply == nil {
		return false, errors.New("apply offline effect callback is nil")
	}
	if applyErr := apply(ctx, effect); applyErr != nil {
		if errors.Is(applyErr, ErrOfflineEffectSuperseded) {
			n, updateErr := qtx.SupersedeOfflineEffect(ctx, foghorndb.SupersedeOfflineEffectParams{EffectID: effect.ID, LeaseToken: effect.LeaseToken})
			if updateErr != nil {
				return false, fmt.Errorf("supersede offline effect after revision CAS: %w", updateErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit superseded offline effect after revision CAS: %w", commitErr)
			}
			return n == 1, nil
		}
		return false, applyErr
	}
	n, err := qtx.CompleteOfflineEffect(ctx, foghorndb.CompleteOfflineEffectParams{EffectID: effect.ID, LeaseToken: effect.LeaseToken})
	if err != nil {
		return false, fmt.Errorf("complete offline effect: %w", err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("commit offline effect: %w", commitErr)
	}
	return n == 1, nil
}

// FailOfflineEffect releases a failed lease with bounded exponential backoff.
func FailOfflineEffect(ctx context.Context, effect OfflineEffect, cause error) error {
	if db == nil || effect.ID <= 0 || effect.LeaseToken == "" {
		return nil
	}
	message := "offline effect failed"
	if cause != nil {
		message = cause.Error()
	}
	err := foghorndb.New(db).FailOfflineEffect(ctx, foghorndb.FailOfflineEffectParams{
		EffectID: effect.ID, LeaseToken: effect.LeaseToken, ErrorMessage: message,
	})
	if err != nil {
		return fmt.Errorf("release failed offline effect: %w", err)
	}
	return nil
}

// PurgeTerminalOfflineEffects deletes applied/superseded offline transitions older than the
// retention window; the pending set is the working state, terminal rows are only diagnostics.
func PurgeTerminalOfflineEffects(ctx context.Context, olderThan time.Duration) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		olderThan = 24 * time.Hour
	}
	n, err := foghorndb.New(db).PurgeTerminalOfflineEffects(ctx, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("purge terminal offline effects: %w", err)
	}
	return n, nil
}
