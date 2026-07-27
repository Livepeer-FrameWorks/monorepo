package control

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lib/pq"
)

// EnqueueStagingCleanupTx durably enqueues a freeze garbage object for deletion, ON the same transaction that
// makes it garbage. The key may be a STAGING object OR a superseded/abandoned published CANDIDATE (media or
// co-located .dtsh) — any object the committing transition orphans (a completion commit, a stale-recovery
// reset, or the terminal identity-clearing trigger). S3 deletion is not transactional with Postgres, so
// enqueuing in-tx + draining with a retry worker (StagingCleanupJob) is what makes cleanup durable — a failed
// or crashed delete is retried from this row instead of leaking unbilled provider storage. Idempotent: ON
// CONFLICT DO NOTHING, so a retried completion or a re-enqueued key is a no-op.
func EnqueueStagingCleanupTx(ctx context.Context, tx *sql.Tx, objectKey string) error {
	if strings.TrimSpace(objectKey) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.staging_cleanup_queue (object_key)
		VALUES ($1)
		ON CONFLICT (object_key) DO NOTHING`, objectKey)
	return err
}

// EnqueueDtshAttemptGarbageTx enqueues BOTH deletable objects a .dtsh attempt can leave behind — its staging
// upload `<key>.dtsh.staging.<req>` AND its promoted-but-unreferenced candidate `<key>.dtsh.att-<req>` — on the
// SAME transaction. A .dtsh completion can promote the versioned candidate and THEN lose the DB CAS (or be
// abandoned), so clearing/failing the attempt must collect the candidate too, not only staging. Idempotent.
func EnqueueDtshAttemptGarbageTx(ctx context.Context, tx *sql.Tx, objectKey, requestID string) error {
	if strings.TrimSpace(objectKey) == "" || strings.TrimSpace(requestID) == "" {
		return nil
	}
	if err := EnqueueStagingCleanupTx(ctx, tx, FreezeStagingKey(objectKey+".dtsh", requestID)); err != nil {
		return err
	}
	return EnqueueStagingCleanupTx(ctx, tx, FreezePublishDtshKey(objectKey, requestID))
}

// RecordPublicationPairDB records, at completion time, the staging + candidate ledger rows for one object pair
// (staging = always garbage once superseded; candidate = garbage only when it is not the live pointer).
//
// Its role differs by which claim path produced the attempt:
//   - MAIN freeze attempts (ClaimFreezeAttempt) already wrote all four deterministic rows atomically with the
//     claim (RecordFreezePublicationLedgerTx), before the node held any PUT URL. For those this call is a
//     redundant re-assertion and a failure here is non-fatal — reconcileFreezePublicationLedger still collects
//     the objects from the claim-time rows.
//   - INCREMENTAL .dtsh-only attempts (claimDtshAttempt) do NOT write the ledger at claim time. For those this
//     completion-time insertion, together with stale-attempt cleanup, IS the durability source for the .dtsh
//     candidate — there is no earlier record to fall back on.
//
// Idempotent via ON CONFLICT.
func RecordPublicationPairDB(ctx context.Context, dbh *sql.DB, artifactHash, tenant, requestID, stagingKey, candidateKey string) error {
	if dbh == nil || strings.TrimSpace(stagingKey) == "" || strings.TrimSpace(candidateKey) == "" {
		return nil
	}
	_, err := dbh.ExecContext(ctx, `
		INSERT INTO foghorn.freeze_publication_ledger (object_key, artifact_hash, tenant_id, request_id, guarded)
		VALUES ($1, $3, $4, $5, false), ($2, $3, $4, $5, true)
		ON CONFLICT (object_key) DO NOTHING`, stagingKey, candidateKey, artifactHash, tenant, requestID)
	return err
}

// RecordFreezePublicationLedgerTx records — ATOMICALLY WITH THE CLAIM, before the node holds any PUT URL — all
// FOUR deterministic objects a freeze attempt can produce: its main + .dtsh staging uploads (guarded=false,
// always garbage once superseded) and its main + .dtsh published candidates (guarded=true, garbage only when
// not the live pointer). Because these rows exist before the objects do, no completion-time ledger write is on
// the critical path for durability: even if a completion's guarded transaction is lost to a concurrent
// duplicate that clears the attempt identity, the sweep still collects whatever the node uploaded/promoted. A
// non-.dtsh attempt simply leaves the .dtsh rows for the sweep to no-op (the objects never existed).
func RecordFreezePublicationLedgerTx(ctx context.Context, tx *sql.Tx, artifactHash, tenant, requestID, objectKey string) error {
	if strings.TrimSpace(objectKey) == "" || strings.TrimSpace(requestID) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO foghorn.freeze_publication_ledger (object_key, artifact_hash, tenant_id, request_id, guarded)
		VALUES ($1, $5, $6, $7, false), ($2, $5, $6, $7, true), ($3, $5, $6, $7, false), ($4, $5, $6, $7, true)
		ON CONFLICT (object_key) DO NOTHING`,
		FreezeStagingKey(objectKey, requestID), FreezePublishKey(objectKey, requestID),
		FreezeStagingKey(objectKey+".dtsh", requestID), FreezePublishDtshKey(objectKey, requestID),
		artifactHash, tenant, requestID)
	return err
}

// ClearPublicationLedgerTx removes this completion's OWN ledger rows ON the committing transaction: the
// candidate it just flipped to active_object_key/active_dtsh_key is live (keep the object), and its staging is
// separately enqueued for cleanup. Deletes strictly by object_key — NOT by request id — so a peer completion of
// the same attempt that published a DIFFERENT object (e.g. a mixed duplicate's orphaned .dtsh candidate) keeps
// its own ledger row for the sweep to reconcile.
func ClearPublicationLedgerTx(ctx context.Context, tx *sql.Tx, keys ...string) error {
	filtered := keys[:0]
	for _, k := range keys {
		if strings.TrimSpace(k) != "" {
			filtered = append(filtered, k)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM foghorn.freeze_publication_ledger WHERE object_key = ANY($1)`, pq.Array(filtered))
	return err
}
