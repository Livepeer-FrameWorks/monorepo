package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.ingest_offline_effects
		   SET leased_until=NULL, lease_token=NULL, attempts=GREATEST(attempts-1, 0),
		       claim_affinity=NULLIF($3, ''), next_attempt_at=NOW(), updated_at=NOW()
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken, authorityInstance)
	if err != nil {
		return fmt.Errorf("release offline effect to leader: %w", err)
	}
	return nil
}

func enqueueOfflineEffectTx(ctx context.Context, tx *sql.Tx, tenantID, internalName, nodeID, generation string, revision int64, intent OfflineEffectIntent) error {
	if revision <= 0 {
		return fmt.Errorf("enqueue offline effect requires positive source revision")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.ingest_offline_effects
			(tenant_id, stream_internal_name, source_node_id, source_generation, source_revision,
			 set_node_offline, teardown_stream, broadcast_offline, decklog_trigger)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, stream_internal_name, source_revision) DO NOTHING
	`, tenantID, internalName, nodeID, generation, revision, intent.SetNodeOffline, intent.TeardownStream, intent.BroadcastOffline, intent.DecklogTrigger)
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
	rows, err := db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			  FROM foghorn.ingest_offline_effects
			 WHERE state = 'pending'
			   AND next_attempt_at <= NOW()
			   AND (leased_until IS NULL OR leased_until < NOW())
			   AND (claim_affinity IS NULL OR claim_affinity = $3
			        OR updated_at <= NOW() - INTERVAL '10 seconds')
			 ORDER BY next_attempt_at, id
			 FOR UPDATE SKIP LOCKED
			 LIMIT $1
		), leased AS (
			UPDATE foghorn.ingest_offline_effects o
			   SET lease_token = gen_random_uuid(),
			       leased_until = NOW() + ($2 * INTERVAL '1 millisecond'),
			       attempts = attempts + 1,
			       claim_affinity = NULL,
			       updated_at = NOW()
			  FROM candidates c
			 WHERE o.id = c.id
			 RETURNING o.id, o.tenant_id::text, o.stream_internal_name, o.source_node_id,
			           COALESCE(o.source_generation::text, ''), o.source_revision,
			           o.set_node_offline, o.teardown_stream, o.broadcast_offline,
			           o.decklog_trigger, o.lease_token::text
		)
		SELECT * FROM leased ORDER BY id
	`, limit, lease.Milliseconds(), instanceID)
	if err != nil {
		return nil, fmt.Errorf("claim offline effects: %w", err)
	}
	defer rows.Close()
	var out []OfflineEffect
	for rows.Next() {
		var e OfflineEffect
		if err := rows.Scan(&e.ID, &e.TenantID, &e.InternalName, &e.NodeID, &e.SourceGeneration,
			&e.SourceRevision, &e.SetNodeOffline, &e.TeardownStream, &e.BroadcastOffline,
			&e.DecklogTrigger, &e.LeaseToken); err != nil {
			return nil, fmt.Errorf("scan offline effect: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
	if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, ingestStreamAdvisoryLockKey(effect.TenantID, effect.InternalName)); lockErr != nil {
		return false, fmt.Errorf("lock offline effect stream: %w", lockErr)
	}
	var ownsLease bool
	err = tx.QueryRowContext(ctx, `
		SELECT true FROM foghorn.ingest_offline_effects
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
		 FOR UPDATE
	`, effect.ID, effect.LeaseToken).Scan(&ownsLease)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("lock offline effect lease: %w", err)
	}
	var active bool
	if probeErr := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM foghorn.ingest_sessions
			 WHERE tenant_id = $1::uuid AND stream_internal_name = $2 AND ended_at IS NULL
		)`, effect.TenantID, effect.InternalName).Scan(&active); probeErr != nil {
		return false, fmt.Errorf("recheck offline effect authority: %w", probeErr)
	}
	if active {
		res, updateErr := tx.ExecContext(ctx, `
			UPDATE foghorn.ingest_offline_effects
			   SET state='superseded', applied_at=NOW(), updated_at=NOW(), leased_until=NULL, lease_token=NULL
			 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
		`, effect.ID, effect.LeaseToken)
		if updateErr != nil {
			return false, fmt.Errorf("supersede offline effect: %w", updateErr)
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return false, fmt.Errorf("supersede offline effect rows: %w", rowsErr)
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
			res, updateErr := tx.ExecContext(ctx, `
				UPDATE foghorn.ingest_offline_effects
				   SET state='superseded', applied_at=NOW(), updated_at=NOW(), leased_until=NULL, lease_token=NULL
				 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
			`, effect.ID, effect.LeaseToken)
			if updateErr != nil {
				return false, fmt.Errorf("supersede offline effect after revision CAS: %w", updateErr)
			}
			n, rowsErr := res.RowsAffected()
			if rowsErr != nil {
				return false, fmt.Errorf("supersede offline effect rows after revision CAS: %w", rowsErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("commit superseded offline effect after revision CAS: %w", commitErr)
			}
			return n == 1, nil
		}
		return false, applyErr
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE foghorn.ingest_offline_effects
		   SET state='applied', applied_at=NOW(), updated_at=NOW(), leased_until=NULL, lease_token=NULL, last_error=NULL
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken)
	if err != nil {
		return false, fmt.Errorf("complete offline effect: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("complete offline effect rows: %w", err)
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
	_, err := db.ExecContext(ctx, `
		UPDATE foghorn.ingest_offline_effects
		   SET leased_until=NULL, lease_token=NULL, last_error=$3, updated_at=NOW(),
		       next_attempt_at=NOW() + LEAST(INTERVAL '5 minutes', INTERVAL '1 second' * power(2, LEAST(attempts, 8)))
		 WHERE id=$1 AND state='pending' AND lease_token=$2::uuid
	`, effect.ID, effect.LeaseToken, message)
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
	res, err := db.ExecContext(ctx, `
		DELETE FROM foghorn.ingest_offline_effects
		 WHERE id IN (
			SELECT id FROM foghorn.ingest_offline_effects
			 WHERE state IN ('applied', 'superseded')
			   AND updated_at < NOW() - ($1 * INTERVAL '1 millisecond')
			 ORDER BY updated_at
			 LIMIT 1000
		 )
	`, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("purge terminal offline effects: %w", err)
	}
	return res.RowsAffected()
}
