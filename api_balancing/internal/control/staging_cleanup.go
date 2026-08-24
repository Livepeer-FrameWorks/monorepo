package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// EnqueueStagingCleanupTx durably enqueues a freeze garbage object for deletion, ON the same transaction that
// makes it garbage. The key may be a STAGING object OR a superseded/abandoned published CANDIDATE (media or
// co-located .dtsh) — any object the committing transition orphans (a completion commit, a stale-recovery
// reset, or the terminal identity-clearing trigger). S3 deletion is not transactional with Postgres, so
// enqueuing in-tx + draining with a retry worker (StagingCleanupJob) is what makes cleanup durable — a failed
// or crashed delete is retried from this row instead of leaking unbilled provider storage. Idempotent: ON CONFLICT the
// row's schedule/lease are unchanged EXCEPT that a missing (NULL) backend owner is filled from this cell's fingerprint,
// so a re-enqueue never leaves the row unattributed for the strict worker.
func EnqueueStagingCleanupTx(ctx context.Context, tx *sql.Tx, objectKey string) error {
	if strings.TrimSpace(objectKey) == "" {
		return nil
	}
	// Capture the backend the object lives on so the cleanup worker deletes from — or fails closed on — the RECORDED
	// store, not whatever is current after a repoint. Freeze garbage is always on THIS cell's current local store at
	// enqueue time. FAIL CLOSED on an empty fingerprint (a cell producing freeze garbage has a local store), and on
	// conflict FILL a pre-existing NULL owner rather than leaving it unattributed for the strict worker.
	bid := localBackendFingerprint()
	if bid == "" {
		return fmt.Errorf("enqueue staging cleanup: no local backend fingerprint to attribute %s", objectKey)
	}
	return foghorndb.New(tx).EnqueueOwnedStagingCleanup(ctx, foghorndb.EnqueueOwnedStagingCleanupParams{
		ObjectKey: objectKey, BackendID: sql.NullString{String: bid, Valid: true},
	})
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
	bid := localBackendFingerprint()
	if bid == "" {
		return fmt.Errorf("record publication pair: no local backend fingerprint to attribute %s", stagingKey)
	}
	return foghorndb.New(dbh).RecordPublicationPair(ctx, foghorndb.RecordPublicationPairParams{
		StagingKey: stagingKey, CandidateKey: candidateKey, ArtifactHash: artifactHash,
		TenantID: tenant, RequestID: requestID, BackendID: sql.NullString{String: bid, Valid: true},
	})
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
	bid := localBackendFingerprint()
	if bid == "" {
		return fmt.Errorf("record freeze publication ledger: no local backend fingerprint to attribute %s", objectKey)
	}
	return foghorndb.New(tx).RecordFreezePublicationLedger(ctx, foghorndb.RecordFreezePublicationLedgerParams{
		StagingKey: FreezeStagingKey(objectKey, requestID), CandidateKey: FreezePublishKey(objectKey, requestID),
		DtshStagingKey: FreezeStagingKey(objectKey+".dtsh", requestID), DtshCandidateKey: FreezePublishDtshKey(objectKey, requestID),
		ArtifactHash: artifactHash, TenantID: tenant, RequestID: requestID, BackendID: sql.NullString{String: bid, Valid: true},
	})
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
	return foghorndb.New(tx).DeleteFreezePublicationLedgerKeys(ctx, filtered)
}
