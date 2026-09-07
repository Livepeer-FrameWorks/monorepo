package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	ID                   int64
	TenantID             string
	InternalName         string
	NodeID               string
	SourceGeneration     string
	SourceRevision       int64
	SetNodeOffline       bool
	SetNodeOfflineDone   bool
	TeardownStream       bool
	TeardownDone         bool
	BroadcastOffline     bool
	BroadcastOfflineDone bool
	DecklogTrigger       []byte
	DecklogDone          bool
	LeaseToken           string
}

// OfflineEffectLegResults records the locally completed legs from one unlocked
// dispatch pass. Teardown completion is intentionally absent: only Helmsman's
// generation-correlated post-stop inventory acknowledgement can settle it.
type OfflineEffectLegResults struct {
	SetNodeOfflineDone   bool
	BroadcastOfflineDone bool
	DecklogDone          bool
}

type OfflineEffectDeadLetter struct {
	ID             int64
	TenantID       string
	InternalName   string
	SourceRevision int64
	LastError      string
}

func FailExhaustedOfflineEffects(ctx context.Context) ([]OfflineEffectDeadLetter, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := foghorndb.New(db).FailExhaustedOfflineEffects(ctx)
	if err != nil {
		return nil, fmt.Errorf("dead-letter exhausted offline effects: %w", err)
	}
	out := make([]OfflineEffectDeadLetter, 0, len(rows))
	for _, row := range rows {
		out = append(out, OfflineEffectDeadLetter{
			ID: row.ID, TenantID: row.TenantID, InternalName: row.StreamInternalName,
			SourceRevision: row.SourceRevision, LastError: row.LastError.String,
		})
	}
	return out, nil
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
	if strings.TrimSpace(generation) == "" {
		// There is no correlation key an acknowledgement could safely echo.
		// Other offline legs still apply; runtime teardown is left to Mist's
		// already-empty inventory/restart reconciliation.
		intent.TeardownStream = false
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
			SetNodeOffline: row.SetNodeOffline, SetNodeOfflineDone: row.SetNodeOfflineDone,
			TeardownStream: row.TeardownStream, TeardownDone: row.TeardownDone,
			BroadcastOffline: row.BroadcastOffline, BroadcastOfflineDone: row.BroadcastOfflineDone,
			DecklogTrigger: row.DecklogTrigger, DecklogDone: row.DecklogDone, LeaseToken: row.LeaseToken,
		})
	}
	return out, nil
}

type offlineLegFlags struct {
	nodeOffline, teardown, broadcast, decklog bool
}

func (f offlineLegFlags) allDone(effect OfflineEffect) bool {
	return (!effect.SetNodeOffline || f.nodeOffline) &&
		(!effect.TeardownStream || f.teardown) &&
		(!effect.BroadcastOffline || f.broadcast) &&
		(len(effect.DecklogTrigger) == 0 || f.decklog)
}

func readOfflineLegsLocked(ctx context.Context, tx *sql.Tx, effect OfflineEffect) (offlineLegFlags, error) {
	row, err := foghorndb.New(tx).ReadOfflineEffectLegsLocked(ctx, foghorndb.ReadOfflineEffectLegsLockedParams{
		EffectID: effect.ID, LeaseToken: effect.LeaseToken,
	})
	return offlineLegFlags{
		nodeOffline: row.SetNodeOfflineDone, teardown: row.TeardownDone,
		broadcast: row.BroadcastOfflineDone, decklog: row.DecklogDone,
	}, err
}

func settleOfflineEffectLocked(ctx context.Context, tx *sql.Tx, effect OfflineEffect, flags offlineLegFlags, releaseLease bool) (bool, error) {
	n, err := foghorndb.New(tx).SettleOfflineEffect(ctx, foghorndb.SettleOfflineEffectParams{
		SetNodeOfflineDone: flags.nodeOffline, TeardownDone: flags.teardown,
		BroadcastOfflineDone: flags.broadcast, DecklogDone: flags.decklog,
		ReleaseLease: releaseLease, EffectID: effect.ID, LeaseToken: effect.LeaseToken,
	})
	if err != nil {
		return false, fmt.Errorf("settle offline effect legs: %w", err)
	}
	return flags.allDone(effect) && n == 1, nil
}

// ApplyClaimedOfflineEffect uses the same three-phase pattern as admission
// effects: verify authority under the stream lock, perform network I/O without
// locks, then merge acknowledgements and completed local legs under the lock.
// This prevents a slow Helmsman call from blocking a reconnect and ensures an
// acknowledgement wait re-dispatches only teardown, never Decklog or federation.
func ApplyClaimedOfflineEffect(ctx context.Context, effect OfflineEffect, apply func(context.Context, OfflineEffect) (OfflineEffectLegResults, error)) (bool, error) {
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
	flags, err := readOfflineLegsLocked(ctx, tx, effect)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("lock offline effect lease: %w", err)
	}
	effect.SetNodeOfflineDone = flags.nodeOffline
	effect.TeardownDone = flags.teardown
	effect.BroadcastOfflineDone = flags.broadcast
	effect.DecklogDone = flags.decklog
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
	if commitErr := tx.Commit(); commitErr != nil {
		return false, fmt.Errorf("commit offline effect authority phase: %w", commitErr)
	}
	if apply == nil {
		return false, errors.New("apply offline effect callback is nil")
	}

	legs, applyErr := apply(ctx, effect)

	tx3, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("begin offline effect settle tx: %w", err))
	}
	defer rollbackQuiet(tx3)
	qtx3 := foghorndb.New(tx3)
	if lockErr := qtx3.AcquireDVRStartLock(ctx, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
		return false, errors.Join(applyErr, fmt.Errorf("lock offline effect stream for settle: %w", lockErr))
	}
	current, err := readOfflineLegsLocked(ctx, tx3, effect)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.Join(applyErr, tx3.Commit())
	}
	if err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("re-read offline effect legs: %w", err))
	}
	active, err = qtx3.HasActiveIngestSession(ctx, foghorndb.HasActiveIngestSessionParams{TenantID: effect.TenantID, StreamInternalName: effect.InternalName})
	if err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("recheck offline effect authority for settle: %w", err))
	}
	if active || errors.Is(applyErr, ErrOfflineEffectSuperseded) {
		n, updateErr := qtx3.SupersedeOfflineEffect(ctx, foghorndb.SupersedeOfflineEffectParams{EffectID: effect.ID, LeaseToken: effect.LeaseToken})
		if updateErr != nil {
			return false, errors.Join(applyErr, fmt.Errorf("supersede offline effect after dispatch: %w", updateErr))
		}
		if commitErr := tx3.Commit(); commitErr != nil {
			return false, errors.Join(applyErr, fmt.Errorf("commit superseded offline effect after dispatch: %w", commitErr))
		}
		return n == 1, nil
	}
	current.nodeOffline = current.nodeOffline || legs.SetNodeOfflineDone
	current.broadcast = current.broadcast || legs.BroadcastOfflineDone
	current.decklog = current.decklog || legs.DecklogDone
	completed, err := settleOfflineEffectLocked(ctx, tx3, effect, current, applyErr == nil)
	if err != nil {
		return false, errors.Join(applyErr, err)
	}
	if commitErr := tx3.Commit(); commitErr != nil {
		return false, errors.Join(applyErr, fmt.Errorf("commit offline effect settle: %w", commitErr))
	}
	if applyErr != nil {
		return false, applyErr
	}
	return completed, nil
}

// MarkOfflineTeardownDone records Helmsman's post-stop PushList convergence
// proof. The authenticated node identity and exact source generation prevent a
// stale or cross-node acknowledgement from completing another teardown.
func MarkOfflineTeardownDone(ctx context.Context, nodeID, sourceGeneration string) error {
	if db == nil {
		return nil
	}
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(sourceGeneration) == "" {
		return errors.New("mark offline teardown requires node and source generation")
	}
	queries := foghorndb.New(db)
	revived, err := queries.ReviveFailedOfflineTeardownDone(ctx, foghorndb.ReviveFailedOfflineTeardownDoneParams{
		SourceNodeID: nodeID, SourceGeneration: sourceGeneration,
	})
	if err != nil {
		return fmt.Errorf("revive failed offline teardown: %w", err)
	}
	if revived > 0 {
		ObserveOfflineEffectDeadLetter("revived")
	}
	_, err = queries.MarkOfflineTeardownDone(ctx, foghorndb.MarkOfflineTeardownDoneParams{
		SourceNodeID: nodeID, SourceGeneration: sourceGeneration,
	})
	if err != nil {
		return fmt.Errorf("mark offline teardown done: %w", err)
	}
	return nil
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
