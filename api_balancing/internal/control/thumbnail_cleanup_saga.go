package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/google/uuid"
)

// RecordStreamCleanupObligation durably records the tombstone + thumbnail-cleanup obligation for a deleted asset
// (a live stream, keyed by asset_key = stream_id, which has no foghorn.artifacts row). One Foghorn database belongs to
// ONE cell and ONE immutable S3 backend, so the obligation has a SINGLE sweep target: this cell's local store, recorded
// as a backend_id fingerprint snapshot read BEFORE any control row is deleted. The row's existence is the durable
// tombstone consulted by AssetTombstoned / the claim, publish, and resolve fences; its pending drain state is worked by
// StreamCleanupJob. Idempotent: a re-delivered obligation for the same asset_key is a no-op (ON CONFLICT DO NOTHING)
// that preserves the ORIGINAL snapshot. FAILS CLOSED (returns an error) on a missing DB or empty identity so the
// obligation is retried, never silently acknowledged as durable.
func RecordStreamCleanupObligation(ctx context.Context, dbh *sql.DB, tenantID, assetKey string) error {
	tenantID = strings.TrimSpace(tenantID)
	assetKey = strings.TrimSpace(assetKey)
	// FAIL CLOSED: a nil return is the caller's proof the tombstone is durable, and the RPC turns it into a
	// positive ack that lets Commodore clear its delivery outbox. A missing DB or an empty identity is NOT a
	// durable record — it must be an error so the obligation is retried, never silently acknowledged.
	if dbh == nil {
		return errors.New("record stream cleanup obligation: no database configured")
	}
	if tenantID == "" || assetKey == "" {
		return errors.New("record stream cleanup obligation: tenant_id and asset_key are required")
	}
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths

	// Per-asset fence: serialize with a concurrent claim/publish for the same asset_key. A row-lock cannot do this
	// (the tombstone row does not exist yet, and FOR UPDATE on an absent row locks nothing), so record/claim/publish
	// share a transaction-scoped advisory lock — whichever commits first, the other observes its result.
	if lErr := lockThumbnailAsset(ctx, tx, assetKey); lErr != nil {
		return lErr
	}

	// Snapshot the backend the asset's thumbnails were written to, INSIDE the tx and BEFORE any control row is dropped.
	// Under one-immutable-backend-per-cell every attempt shares this cell's store, so the DISTINCT recorded non-empty
	// backend_id is unique — assert it rather than picking one arbitrarily. FAIL CLOSED on more than one distinct id:
	// that violates the invariant (the drainer sweeps a single store), and snapshotting an arbitrary one would leak the
	// other. An asset with no recorded id (never published / legacy) falls back to this cell's current local fingerprint
	// (the store its live-stream thumbnails were minted on). The drainer later fails closed if the recorded id no longer
	// matches the cell's current store (a forbidden repoint).
	qtx := foghorndb.New(tx)
	snapshot, sErr := qtx.GetThumbnailAssetBackendSnapshot(ctx, foghorndb.GetThumbnailAssetBackendSnapshotParams{
		TenantID: tenantID, AssetKey: assetKey,
	})
	if sErr != nil {
		return sErr
	}
	distinctBackends := int(snapshot.DistinctBackends)
	recorded := snapshot.BackendID
	if distinctBackends > 1 {
		return fmt.Errorf("record stream cleanup obligation: asset %s has %d distinct thumbnail backend_ids — the one-immutable-backend-per-cell invariant is violated; refusing to snapshot an arbitrary backend", assetKey, distinctBackends)
	}
	backendID := recorded
	if backendID == "" {
		backendID = localBackendFingerprint()
	}
	// FAIL CLOSED on an unattributable obligation: with neither a recorded thumbnail backend nor a local fingerprint we
	// cannot record WHICH store owns these bytes, and the drainer would otherwise settle the durable obligation after a
	// guessed-current-store sweep. Refuse rather than persist a NULL identity (never a silent success).
	if backendID == "" {
		return fmt.Errorf("record stream cleanup obligation: asset %s has no recorded thumbnail backend and this cell has no local fingerprint — refusing to record an unattributed obligation", assetKey)
	}

	// Parent tombstone: existence fences claims/publishes; the lease/status/backoff machinery lives here.
	_, iErr := qtx.InsertStreamCleanupObligation(ctx, foghorndb.InsertStreamCleanupObligationParams{
		AssetKey: assetKey, TenantID: tenantID, BackendID: sql.NullString{String: backendID, Valid: true},
	})
	if iErr != nil {
		return iErr
	}
	// IDEMPOTENT: a fresh insert commits the new tombstone; a re-delivered DeleteStreamThumbnails RPC (RowsAffected==0,
	// the tombstone already exists) commits nothing new but releases the advisory lock as the durable ack, preserving
	// the original snapshot. FAIL CLOSED if RowsAffected itself errors rather than assuming a state — Postgres normally
	// supports it, but a foundational durability record must not proceed on an unknown insert result.
	return tx.Commit()
}

// AssetTombstoned reports whether an asset_key has a durable cleanup tombstone (a stream_cleanup_obligation row,
// pending OR cleaned). This is the fence for assets with NO foghorn.artifacts row (live streams) — the
// artifact-terminal-status fences never fire for them. Non-locking; used where a plain existence read suffices.
func AssetTombstoned(ctx context.Context, dbh *sql.DB, assetKey string) (bool, error) {
	assetKey = strings.TrimSpace(assetKey)
	if dbh == nil || assetKey == "" {
		return false, nil
	}
	return foghorndb.New(dbh).AssetTombstoned(ctx, assetKey)
}

// assetTombstonedTx reports whether a tombstone exists for the asset, read inside a guarded claim/publish
// transaction. Correctness against a concurrent RecordStreamCleanupObligation comes from lockThumbnailAsset (the
// shared per-asset advisory lock the caller MUST take first), not from a row lock — a row lock cannot fence an
// as-yet-uninserted row. Under the advisory lock this plain read observes the committed tombstone-or-not.
func assetTombstonedTx(ctx context.Context, tx *sql.Tx, assetKey string) (bool, error) {
	_, err := foghorndb.New(tx).GetAssetTombstone(ctx, assetKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// The remote cleanup phase is bounded to two minutes by the worker. Keeping
// the durable claim for three minutes prevents a replacement worker from
// overtaking an operation whose cancellation is still propagating.
const federatedPointerPurgeLease = 3 * time.Minute

// lockThumbnailAsset takes a transaction-scoped advisory lock keyed by asset_key so RecordStreamCleanupObligation,
// ClaimThumbnailAttempt, and PublishThumbnailAttempt for the SAME asset serialize. This closes the insert-vs-read
// race: without it a claim/publish can read "no tombstone", a deletion can commit one, and the claim/publish can
// then commit and grant upload authority / flip the pointer for an already-deleted asset. hashtext maps the
// asset_key to an int4 bucket; the lock releases automatically at transaction end. Hash collisions only cause two
// unrelated assets to briefly serialize — never a correctness loss.
func lockThumbnailAsset(ctx context.Context, tx *sql.Tx, assetKey string) error {
	return foghorndb.New(tx).LockThumbnailAsset(ctx, foghorndb.LockThumbnailAssetParams{
		LockNamespace: artifacts.ThumbnailAssetLockNamespace, AssetKey: assetKey,
	})
}

// LockThumbnailAssetTx exposes the transaction-scoped asset fence to jobs that
// compose thumbnail cleanup with service-owned rows in the same transaction.
// It must be the first lock taken for the asset so tombstone recording,
// publication, and cleanup all follow asset -> service row -> assignment ->
// pointer/object -> cleanup queue.
func LockThumbnailAssetTx(ctx context.Context, tx *sql.Tx, assetKey string) error {
	if tx == nil || strings.TrimSpace(assetKey) == "" {
		return nil
	}
	return lockThumbnailAsset(ctx, tx, assetKey)
}

// FederatedPointerPurgeKind selects the signed-authority predicate that must
// still hold when a replaceable pointer is fenced for deletion.
type FederatedPointerPurgeKind uint8

const (
	FederatedPointerPurgeTombstone FederatedPointerPurgeKind = iota + 1
	FederatedPointerPurgeStale
	FederatedPointerPurgeInterruptedActive
)

type FederatedPointerPurgeSettlement uint8

const (
	FederatedPointerPurgeNotOwned FederatedPointerPurgeSettlement = iota
	FederatedPointerPurgeDeleted
	FederatedPointerPurgeRestoredActive
)

// FenceFederatedPointerForPurge serializes with thumbnail claim/publication
// for the same asset, rechecks the authority and dependency guards, and makes
// the parent terminal before any byte sweep begins. Once this commits, a new
// thumbnail attempt cannot appear behind the purge worker's snapshot.
func FenceFederatedPointerForPurge(ctx context.Context, db *sql.DB, tenantID, assetKey, retentionInterval string, kind FederatedPointerPurgeKind, allowCrossClusterDelete bool) (string, bool, error) {
	if db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" {
		return "", false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lockErr := lockThumbnailAsset(ctx, tx, assetKey); lockErr != nil {
		return "", false, lockErr
	}
	q := foghorndb.New(tx)
	destinations, err := q.ListThumbnailDestinations(ctx, foghorndb.ListThumbnailDestinationsParams{
		TenantID: tenantID, AssetKey: assetKey,
	})
	if err != nil {
		return "", false, err
	}
	for _, destination := range destinations {
		if !destination.BackendLocal && !allowCrossClusterDelete {
			// Do not make a pointer terminal before this cell can execute every known byte
			// deletion. A later run may fence it after federation mutations are enabled.
			return "", false, nil
		}
	}
	claimToken := uuid.NewString()
	leaseInterval := federatedPointerPurgeLease.String()
	var affected int64
	switch kind {
	case FederatedPointerPurgeTombstone:
		affected, err = q.FenceTombstonedFederatedArtifactPointerForPurge(ctx, foghorndb.FenceTombstonedFederatedArtifactPointerForPurgeParams{
			PurgeToken: claimToken, LeaseInterval: leaseInterval,
			ArtifactHash: assetKey, TenantID: tenantID, RetentionInterval: retentionInterval,
		})
	case FederatedPointerPurgeStale:
		affected, err = q.FenceStaleFederatedArtifactPointerForPurge(ctx, foghorndb.FenceStaleFederatedArtifactPointerForPurgeParams{
			PurgeToken: claimToken, LeaseInterval: leaseInterval,
			ArtifactHash: assetKey, TenantID: tenantID, RetentionInterval: retentionInterval,
		})
	case FederatedPointerPurgeInterruptedActive:
		affected, err = q.FenceInterruptedActiveFederatedArtifactPointerPurge(ctx, foghorndb.FenceInterruptedActiveFederatedArtifactPointerPurgeParams{
			PurgeToken: claimToken, LeaseInterval: leaseInterval,
			ArtifactHash: assetKey, TenantID: tenantID,
		})
	default:
		return "", false, errors.New("unknown federated pointer purge kind")
	}
	if err != nil {
		return "", false, err
	}
	if affected == 0 {
		return "", false, nil
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return claimToken, true, nil
}

// FinalizeFederatedPointerPurge removes thumbnail control state and its terminal
// parent in one transaction. If the parent's authority/dependency predicate no
// longer holds, the transaction rolls back so a later retry retains all routing
// evidence needed to sweep the bytes safely.
func FinalizeFederatedPointerPurge(ctx context.Context, db *sql.DB, tenantID, assetKey, claimToken string) (FederatedPointerPurgeSettlement, error) {
	if db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" || strings.TrimSpace(claimToken) == "" {
		return FederatedPointerPurgeNotOwned, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return FederatedPointerPurgeNotOwned, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lockErr := lockThumbnailAsset(ctx, tx, assetKey); lockErr != nil {
		return FederatedPointerPurgeNotOwned, lockErr
	}
	q := foghorndb.New(tx)
	if _, stateErr := q.GetFencedFederatedArtifactPointerPurgeState(ctx, foghorndb.GetFencedFederatedArtifactPointerPurgeStateParams{
		ArtifactHash: assetKey, TenantID: tenantID, PurgeToken: claimToken,
	}); errors.Is(stateErr, sql.ErrNoRows) {
		return FederatedPointerPurgeNotOwned, nil
	} else if stateErr != nil {
		return FederatedPointerPurgeNotOwned, stateErr
	}
	if cleanupErr := DeleteThumbnailControlRowsTx(ctx, tx, tenantID, assetKey); cleanupErr != nil {
		return FederatedPointerPurgeNotOwned, cleanupErr
	}
	restored, err := q.RestoreClaimedFederatedArtifactPointerAfterActiveAuthority(ctx, foghorndb.RestoreClaimedFederatedArtifactPointerAfterActiveAuthorityParams{
		ArtifactHash: assetKey, TenantID: tenantID, PurgeToken: claimToken,
	})
	if err != nil {
		return FederatedPointerPurgeNotOwned, err
	}
	settlement := FederatedPointerPurgeRestoredActive
	if restored != 1 {
		deleted, deleteErr := q.DeleteFencedFederatedArtifactPointer(ctx, foghorndb.DeleteFencedFederatedArtifactPointerParams{
			ArtifactHash: assetKey, TenantID: tenantID, PurgeToken: claimToken,
		})
		if deleteErr != nil {
			return FederatedPointerPurgeNotOwned, deleteErr
		}
		if deleted != 1 {
			return FederatedPointerPurgeNotOwned, nil
		}
		settlement = FederatedPointerPurgeDeleted
	}
	if err := tx.Commit(); err != nil {
		return FederatedPointerPurgeNotOwned, err
	}
	return settlement, nil
}

// ReleaseFederatedPointerPurgeClaim makes a failed attempt immediately
// reclaimable while retaining its token and terminal pointer state. Byte
// effects may be partial or unknown, so only successful settlement may restore
// active routing or delete the pointer.
func ReleaseFederatedPointerPurgeClaim(ctx context.Context, db *sql.DB, tenantID, assetKey, claimToken string) (bool, error) {
	if db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" || strings.TrimSpace(claimToken) == "" {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lockErr := lockThumbnailAsset(ctx, tx, assetKey); lockErr != nil {
		return false, lockErr
	}
	released, err := foghorndb.New(tx).ReleaseFederatedArtifactPointerPurgeClaim(ctx, foghorndb.ReleaseFederatedArtifactPointerPurgeClaimParams{
		ArtifactHash: assetKey, TenantID: tenantID, PurgeToken: claimToken,
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return released == 1, nil
}

// DeferFederatedPointerPurgeClaim retains the terminal token fence but moves
// deterministic repair work out of the hot recovery loop. It is token-bound,
// so an obsolete worker cannot delay a replacement claim.
func DeferFederatedPointerPurgeClaim(ctx context.Context, db *sql.DB, tenantID, assetKey, claimToken string, retryAfter time.Duration) (bool, error) {
	if db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" || strings.TrimSpace(claimToken) == "" || retryAfter <= 0 {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lockErr := lockThumbnailAsset(ctx, tx, assetKey); lockErr != nil {
		return false, lockErr
	}
	deferred, err := foghorndb.New(tx).DeferFederatedArtifactPointerPurgeClaim(ctx, foghorndb.DeferFederatedArtifactPointerPurgeClaimParams{
		ArtifactHash: assetKey, TenantID: tenantID, PurgeToken: claimToken, RetryInterval: retryAfter.String(),
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return deferred == 1, nil
}
