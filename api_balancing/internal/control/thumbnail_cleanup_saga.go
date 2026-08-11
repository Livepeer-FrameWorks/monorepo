package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	var distinctBackends int
	var recorded sql.NullString
	if sErr := tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT backend_id), MIN(backend_id)
		  FROM foghorn.thumbnail_task_assignment
		 WHERE tenant_id = $1 AND asset_key = $2 AND backend_id IS NOT NULL AND backend_id <> ''
	`, tenantID, assetKey).Scan(&distinctBackends, &recorded); sErr != nil {
		return sErr
	}
	if distinctBackends > 1 {
		return fmt.Errorf("record stream cleanup obligation: asset %s has %d distinct thumbnail backend_ids — the one-immutable-backend-per-cell invariant is violated; refusing to snapshot an arbitrary backend", assetKey, distinctBackends)
	}
	backendID := recorded.String
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
	insRes, iErr := tx.ExecContext(ctx, `
		INSERT INTO foghorn.stream_cleanup_obligation (asset_key, tenant_id, backend_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (asset_key) DO NOTHING
	`, assetKey, tenantID, backendID)
	if iErr != nil {
		return iErr
	}
	// IDEMPOTENT: a fresh insert commits the new tombstone; a re-delivered DeleteStreamThumbnails RPC (RowsAffected==0,
	// the tombstone already exists) commits nothing new but releases the advisory lock as the durable ack, preserving
	// the original snapshot. FAIL CLOSED if RowsAffected itself errors rather than assuming a state — Postgres normally
	// supports it, but a foundational durability record must not proceed on an unknown insert result.
	if _, raErr := insRes.RowsAffected(); raErr != nil {
		return raErr
	}
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
	var exists bool
	err := dbh.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1)`, assetKey).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// assetTombstonedTx reports whether a tombstone exists for the asset, read inside a guarded claim/publish
// transaction. Correctness against a concurrent RecordStreamCleanupObligation comes from lockThumbnailAsset (the
// shared per-asset advisory lock the caller MUST take first), not from a row lock — a row lock cannot fence an
// as-yet-uninserted row. Under the advisory lock this plain read observes the committed tombstone-or-not.
func assetTombstonedTx(ctx context.Context, tx *sql.Tx, assetKey string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1`, assetKey).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// thumbnailAssetLockNamespace namespaces the per-asset advisory lock (two-int32 form) so it cannot collide with
// any other pg_advisory_xact_lock in the schema. Arbitrary fixed constant ("tmbl").
const thumbnailAssetLockNamespace = 0x746d626c

// lockThumbnailAsset takes a transaction-scoped advisory lock keyed by asset_key so RecordStreamCleanupObligation,
// ClaimThumbnailAttempt, and PublishThumbnailAttempt for the SAME asset serialize. This closes the insert-vs-read
// race: without it a claim/publish can read "no tombstone", a deletion can commit one, and the claim/publish can
// then commit and grant upload authority / flip the pointer for an already-deleted asset. hashtext maps the
// asset_key to an int4 bucket; the lock releases automatically at transaction end. Hash collisions only cause two
// unrelated assets to briefly serialize — never a correctness loss.
func lockThumbnailAsset(ctx context.Context, tx *sql.Tx, assetKey string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, thumbnailAssetLockNamespace, assetKey)
	return err
}
