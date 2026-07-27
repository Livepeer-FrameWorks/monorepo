package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Thumbnail crash-safe publication repo. Server-minted, node-bound attempts publish to per-attempt STAGING keys,
// get verified (provider-observed), promote to IMMUTABLE version keys, then flip an active pointer — keyed by the
// globally-unique asset_key — via a guarded, monotonic CAS. The version segment IS the attempt id (crypto-rand,
// via mintAttemptID) — one id identifies the attempt AND its immutable object set. tenant_id is carried as
// ownership/authorization attribution on every mutation, never as part of the resource identity.

// affectedOne reports whether exactly one row changed, surfacing any RowsAffected error.
func affectedOne(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ThumbnailStagingKey is the per-attempt upload target: never served, always garbage once the attempt ends.
func ThumbnailStagingKey(assetKey, attemptID, file string) string {
	return "thumbnails/" + assetKey + "/.staging/" + attemptID + "/" + file
}

// ThumbnailVersionKey is the immutable published object for a version (= attempt id). Never overwritten, so a
// stale attempt can never corrupt a live object; the active pointer alone decides which version is served.
func ThumbnailVersionKey(assetKey, version, file string) string {
	return "thumbnails/" + assetKey + "/v/" + version + "/" + file
}

// ThumbnailObject is one allowlisted file an attempt owns.
type ThumbnailObject struct {
	FileName   string
	StagingKey string
	VersionKey string
	ETag       string
	SizeBytes  int64
	Verified   bool
}

// ThumbnailAssignment is a server-minted publication attempt.
type ThumbnailAssignment struct {
	AttemptID          string
	TenantID           string
	AssetKey           string
	NodeID             string
	DestinationCluster string
	Status             string
	Version            string
	Expiry             time.Time
}

// ClaimThumbnailAttempt persists a new attempt (status='assigned') and its per-file object rows (staging keys)
// in ONE transaction, BEFORE the node holds any PUT URL. The version segment is the attempt id. Fail-closed on
// any incomplete identity or empty file set. Returns claimed=false (no error) on a fail-closed reject.
func ClaimThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID, tenantID, assetKey, nodeID, destinationCluster string, files []string, expiry time.Time) (claimed bool, err error) {
	if dbh == nil || attemptID == "" || tenantID == "" || assetKey == "" || nodeID == "" || destinationCluster == "" || len(files) == 0 {
		return false, nil
	}
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths

	// TERMINAL-PARENT FENCE at claim: never hand out upload authority for an artifact that is already terminal
	// (deleted/failed/expired/aborted) — a later publish would be fenced anyway, but only after the node
	// uploaded garbage to staging. Locked FOR UPDATE so a concurrent purge/soft-delete serializes. A live
	// stream_id has no artifact row (not found → proceed).
	var parentTerminal bool
	tErr := tx.QueryRowContext(ctx, `SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE`, assetKey).Scan(&parentTerminal)
	if tErr != nil && tErr != sql.ErrNoRows {
		return false, tErr
	}
	if tErr == nil && parentTerminal {
		return false, nil // fail-closed: parent is terminal, no upload authority
	}

	if _, execErr := tx.ExecContext(ctx, `
		INSERT INTO foghorn.thumbnail_task_assignment
			(attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry)
		VALUES ($1, $2, $3, $4, $5, 'assigned', $1, $6)
	`, attemptID, tenantID, assetKey, nodeID, destinationCluster, expiry); execErr != nil {
		return false, execErr
	}
	for _, f := range files {
		if _, execErr := tx.ExecContext(ctx, `
			INSERT INTO foghorn.thumbnail_task_object (attempt_id, file_name, staging_key)
			VALUES ($1, $2, $3)
		`, attemptID, f, ThumbnailStagingKey(assetKey, attemptID, f)); execErr != nil {
			return false, execErr
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, commitErr
	}
	return true, nil
}

// LoadThumbnailAttempt returns the assignment + its object rows for a completion to bind against. found=false
// when no assignment exists for the attempt (the node echoed an unknown/expired-swept id).
func LoadThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string) (ThumbnailAssignment, []ThumbnailObject, bool, error) {
	var a ThumbnailAssignment
	if dbh == nil || attemptID == "" {
		return a, nil, false, nil
	}
	err := dbh.QueryRowContext(ctx, `
		SELECT attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry
		  FROM foghorn.thumbnail_task_assignment
		 WHERE attempt_id = $1
	`, attemptID).Scan(&a.AttemptID, &a.TenantID, &a.AssetKey, &a.NodeID, &a.DestinationCluster, &a.Status, &a.Version, &a.Expiry)
	if err == sql.ErrNoRows {
		return a, nil, false, nil
	}
	if err != nil {
		return a, nil, false, err
	}
	rows, qErr := dbh.QueryContext(ctx, `
		SELECT file_name, staging_key, version_key, etag, size_bytes, verified
		  FROM foghorn.thumbnail_task_object
		 WHERE attempt_id = $1
		 ORDER BY file_name
	`, attemptID)
	if qErr != nil {
		return a, nil, false, qErr
	}
	defer rows.Close()
	var objs []ThumbnailObject
	for rows.Next() {
		var o ThumbnailObject
		if scanErr := rows.Scan(&o.FileName, &o.StagingKey, &o.VersionKey, &o.ETag, &o.SizeBytes, &o.Verified); scanErr != nil {
			return a, nil, false, scanErr
		}
		objs = append(objs, o)
	}
	return a, objs, true, rows.Err()
}

// MarkThumbnailObjectVerified records the provider-observed identity of a promoted object (version_key + etag +
// size) and marks it verified. FENCED on the owning assignment still being in a non-terminal, non-expired state
// (moved=false otherwise): a concurrent recovery that failed the attempt, or an expired attempt, must not have
// its objects recorded — so a promoted candidate can never be attached to a dead assignment. Provider values
// are authoritative; the completion HEAD-verifies staging and promotes to the version key before calling this.
func MarkThumbnailObjectVerified(ctx context.Context, dbh *sql.DB, attemptID, file, versionKey, etag string, size int64) (bool, error) {
	if dbh == nil {
		return false, nil
	}
	res, err := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_object o
		   SET version_key = $3, etag = $4, size_bytes = $5, verified = true
		 WHERE o.attempt_id = $1 AND o.file_name = $2
		   AND EXISTS (
		         SELECT 1 FROM foghorn.thumbnail_task_assignment a
		          WHERE a.attempt_id = o.attempt_id
		            AND a.status IN ('assigned', 'uploading', 'verifying', 'publishing')
		            AND a.expiry > NOW())
	`, attemptID, file, versionKey, etag, size)
	if err != nil {
		return false, err
	}
	return affectedOne(res)
}

// TransitionThumbnailStatus performs a GUARDED status transition (only from the expected current state). A
// stale/superseded attempt whose status has moved matches zero rows → moved=false, so it cannot re-drive the
// machine. Terminal states ('published'/'failed') are reached only through this guard.
func TransitionThumbnailStatus(ctx context.Context, dbh *sql.DB, attemptID, from, to string) (moved bool, err error) {
	if dbh == nil {
		return false, nil
	}
	res, execErr := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment
		   SET status = $3, updated_at = NOW()
		 WHERE attempt_id = $1 AND status = $2
	`, attemptID, from, to)
	if execErr != nil {
		return false, execErr
	}
	return affectedOne(res)
}

// artifactTerminalStatusSQL is the CANONICAL set of artifact statuses that permanently stop thumbnail publication
// AND resolution: the parent is gone or will never serve media. It mirrors the artifact state machine's terminal
// set used elsewhere (e.g. dvr_chapters_repo.go, the catalog trigger) — 'deleted'/'expired'/'aborted' are the
// gone states, 'failed' the no-media state. Keep this the single source used by claim/publish/resolve/cleanup.
const artifactTerminalStatusSQL = "('deleted', 'failed', 'expired', 'aborted')"

// parentArtifactTombstoned reports whether the artifact an asset_key names is in a TERMINAL state (see
// artifactTerminalStatusSQL) that must stop thumbnail publication. A live stream_id has no artifact row (returns
// false). Used to fence thumbnail promotion/publication so a completion never publishes for a gone/dead parent
// (and never writes version objects into a prefix a purge is about to sweep).
func parentArtifactTombstoned(ctx context.Context, dbh *sql.DB, assetKey string) (bool, error) {
	if dbh == nil || strings.TrimSpace(assetKey) == "" {
		return false, nil
	}
	var terminal bool
	err := dbh.QueryRowContext(ctx, `SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1`, assetKey).Scan(&terminal)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return terminal, nil
}

// FailThumbnailAttempt terminally fails a non-terminal attempt (e.g. a mint that could not prepare every
// presigned URL, so no partial assignment is dispatched). Idempotent; a terminal attempt is left unchanged.
func FailThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string) error {
	if dbh == nil {
		return nil
	}
	_, err := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW()
		 WHERE attempt_id = $1 AND status NOT IN ('published', 'failed')
	`, attemptID)
	return err
}

// EnterThumbnailPublishing moves an in-flight attempt (assigned/uploading/verifying) into 'publishing' — the
// durable state that gates the pointer CAS and blocks re-entry through the earlier states. Idempotent for an
// attempt already 'publishing' (matches zero rows). Terminal attempts ('published'/'failed') are not moved.
func EnterThumbnailPublishing(ctx context.Context, dbh *sql.DB, attemptID string) (moved bool, err error) {
	if dbh == nil {
		return false, nil
	}
	res, execErr := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment
		   SET status = 'publishing', updated_at = NOW()
		 WHERE attempt_id = $1 AND status IN ('assigned', 'uploading', 'verifying') AND expiry > NOW()
	`, attemptID)
	if execErr != nil {
		return false, execErr
	}
	return affectedOne(res)
}

// settleFailedWithVersionCleanup marks a still-'publishing' attempt 'failed' and enqueues its promoted version
// objects for durable cleanup, IN the given transaction — so a non-activating attempt (a stale loser or one
// whose parent was tombstoned) never leaves its promoted objects orphaned. Must run under the attempt row lock.
func settleFailedWithVersionCleanup(ctx context.Context, tx *sql.Tx, attemptID string) error {
	// Reconstruct the version keys deterministically so a promoted-but-unrecorded object is still reclaimed.
	_, versionKeys, kErr := reconstructAttemptObjectKeys(ctx, tx, attemptID)
	if kErr != nil {
		return kErr
	}
	if eErr := EnqueueThumbnailCleanup(ctx, tx, versionKeys); eErr != nil {
		return eErr
	}
	_, mErr := tx.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW()
		 WHERE attempt_id = $1 AND status = 'publishing'
	`, attemptID)
	return mErr
}

// PublishThumbnailAttempt is the atomic pointer switch. In ONE transaction it: (1) LOCKS the attempt row and
// guards it is in 'publishing', unexpired, fully verified, and its parent artifact is not tombstoned; (2) flips
// the active pointer (keyed by the globally-unique asset_key) to this attempt's version via a MONOTONIC CAS — a
// stale attempt (created before the pointer's current attempt) does NOT regress it; (3) marks the attempt
// 'published' and commits the durable side effects atomically. Returns activated=true iff the pointer now serves
// this attempt's version. A non-'publishing', expired, unverified, or tombstoned attempt returns (false, nil)
// with no pointer change; a non-activating attempt is settled 'failed' with its version objects enqueued for
// cleanup so it never leaks. asset_key is the globally-unique identity; tenant_id is ownership attribution.
func PublishThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string) (activated bool, err error) {
	if dbh == nil || attemptID == "" {
		return false, nil
	}
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths

	// LOCK the attempt row for the whole transaction and re-check its guards under the lock: a concurrent
	// expiry-recovery sweep can then neither fail+enqueue this attempt between the read and the pointer flip nor
	// be raced by it. Expired attempts are NOT published here (recovery owns them) — entering 'publishing' before
	// expiry does not license publishing after it.
	var assetKey, tenantID string
	selErr := tx.QueryRowContext(ctx, `
		SELECT asset_key, tenant_id
		  FROM foghorn.thumbnail_task_assignment
		 WHERE attempt_id = $1 AND status = 'publishing' AND expiry > NOW()
		 FOR UPDATE
	`, attemptID).Scan(&assetKey, &tenantID)
	if selErr == sql.ErrNoRows {
		return false, nil // not eligible: not publishing, expired, or already terminal
	}
	if selErr != nil {
		return false, selErr
	}

	var unverified int
	if cErr := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM foghorn.thumbnail_task_object WHERE attempt_id = $1 AND verified = false
	`, attemptID).Scan(&unverified); cErr != nil {
		return false, cErr
	}
	if unverified > 0 {
		return false, nil // cannot publish an attempt with an unverified object
	}

	// PARENT-TOMBSTONE FENCE: never activate a thumbnail whose parent artifact is being purged, or a completion
	// racing a purge could flip the pointer to a version the purge is deleting. The row is locked FOR UPDATE so
	// this serializes against the soft-delete UPDATE and the hard-delete — a publisher that observed 'ready'
	// commits its pointer BEFORE the artifact can be marked deleted, and one that races the deletion sees
	// 'deleted' and settles failed. Read by artifact_hash alone (globally unique + single-owner) so the fence
	// catches the tombstone regardless of tenant; a live stream_id has no artifact row (not found → proceed).
	var artifactTerminal bool
	tErr := tx.QueryRowContext(ctx, `SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE`, assetKey).Scan(&artifactTerminal)
	if tErr != nil && tErr != sql.ErrNoRows {
		return false, tErr
	}
	if tErr == nil && artifactTerminal {
		if sErr := settleFailedWithVersionCleanup(ctx, tx, attemptID); sErr != nil {
			return false, sErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, commitErr
		}
		return false, nil
	}

	// Capture the currently-active version (if any) BEFORE the CAS so we can stamp its supersession time when
	// this attempt displaces it — the reader-safety horizon the GC honors is measured from that stamp. asset_key
	// is globally unique, so this is a single-row read.
	var priorVersion sql.NullString
	if sErr := tx.QueryRowContext(ctx, `SELECT active_version FROM foghorn.thumbnail_active_pointer WHERE asset_key = $1`, assetKey).Scan(&priorVersion); sErr != nil && sErr != sql.ErrNoRows {
		return false, sErr
	}

	// Monotonic pointer CAS keyed by the globally-unique asset_key: advance only when this attempt is at least as
	// new as the pointer's current attempt by the server-owned STRICTLY-MONOTONIC claim_seq (never created_at,
	// which is not a total order across equal timestamps / HA clock skew). `>=` keeps re-publishing the SAME
	// attempt idempotent (equal claim_seq → still activates) while two DISTINCT attempts always compare strictly
	// (distinct sequence values), so a stale attempt can never win and two attempts can never each replace the
	// other. The tenant_id guard is defence-in-depth ownership attribution: asset_key is single-owner so it
	// always matches, but a mismatched write can never hijack another owner's pointer.
	res, upErr := tx.ExecContext(ctx, `
		INSERT INTO foghorn.thumbnail_active_pointer (asset_key, tenant_id, active_version, updated_at)
		VALUES ($2, $3, $1, NOW())
		ON CONFLICT (asset_key) DO UPDATE
		   SET active_version = EXCLUDED.active_version, tenant_id = EXCLUDED.tenant_id, updated_at = NOW()
		 WHERE foghorn.thumbnail_active_pointer.tenant_id = EXCLUDED.tenant_id
		   AND (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = EXCLUDED.active_version)
			 >= (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = foghorn.thumbnail_active_pointer.active_version)
	`, attemptID, assetKey, tenantID)
	if upErr != nil {
		return false, upErr
	}
	activated, aErr := affectedOne(res)
	if aErr != nil {
		return false, aErr
	}

	if activated {
		// Winner: stamp the displaced version's supersession time (the GC horizon anchor) and mark published.
		if priorVersion.Valid && priorVersion.String != "" && priorVersion.String != attemptID {
			if _, sErr := tx.ExecContext(ctx, `
				UPDATE foghorn.thumbnail_task_assignment SET superseded_at = NOW() WHERE attempt_id = $1
			`, priorVersion.String); sErr != nil {
				return false, sErr
			}
		}
		// Under the row lock this must affect exactly one row; a 0-row result means the guarded state changed
		// despite the lock — roll back rather than commit a pointer flip whose attempt is not 'published'.
		pubRes, mErr := tx.ExecContext(ctx, `
			UPDATE foghorn.thumbnail_task_assignment SET status = 'published', updated_at = NOW()
			 WHERE attempt_id = $1 AND status = 'publishing'
		`, attemptID)
		if mErr != nil {
			return false, mErr
		}
		if ok, oErr := affectedOne(pubRes); oErr != nil {
			return false, oErr
		} else if !ok {
			return false, fmt.Errorf("thumbnail publish: attempt %s left 'publishing' under lock; rolling back", attemptID)
		}
		// CRASH-CONVERGENCE: commit the DURABLE side effects ATOMICALLY with the pointer flip, so a crash right
		// after the CAS can never leave them permanently skipped (recovery only re-drives 'publishing', and a
		// duplicate completion returns idempotently). (1) Flip has_thumbnails for the artifact this asset_key
		// names — a no-op for a live stream_id (no artifact row). (2) Enqueue the now-superseded staging objects
		// (garbage once promoted). The Chandler cache invalidation stays best-effort OUTSIDE the tx because it
		// self-heals via Chandler's short TTL + cold-miss resolve.
		if _, hErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts SET has_thumbnails = true, updated_at = NOW()
			 WHERE artifact_hash = $1 AND tenant_id::text = $2 AND has_thumbnails IS DISTINCT FROM true
		`, assetKey, tenantID); hErr != nil {
			return false, hErr
		}
		stagingKeys, sErr := txObjectKeys(ctx, tx, attemptID, "staging_key")
		if sErr != nil {
			return false, sErr
		}
		if eErr := EnqueueThumbnailCleanup(ctx, tx, stagingKeys); eErr != nil {
			return false, eErr
		}
	} else {
		// Non-activating attempt (a newer version owns the pointer, or the ownership guard rejected it): settle
		// it 'failed' with its promoted version objects enqueued for cleanup, so it never leaks.
		if sErr := settleFailedWithVersionCleanup(ctx, tx, attemptID); sErr != nil {
			return false, sErr
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return false, commitErr
	}
	return activated, nil
}

// sqlExecer is satisfied by both *sql.DB and *sql.Tx, so cleanup enqueue can run inside a transaction that
// ALSO performs the guarded terminal transition — the two must be atomic (see RecoverStuckThumbnailAttempts).
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// EnqueueThumbnailCleanup durably enqueues object keys for S3 deletion by the shared staging-cleanup worker.
// Idempotent (ON CONFLICT DO NOTHING); empty keys are skipped. Used for staged objects (garbage once promoted)
// and for the version objects of an abandoned/superseded attempt. MUST run in the SAME transaction as the
// guarded status change that authorizes the deletion — never before it — or a concurrent completion that
// activates the version leaves a live object queued for deletion.
func EnqueueThumbnailCleanup(ctx context.Context, ex sqlExecer, objectKeys []string) error {
	if ex == nil {
		return nil
	}
	for _, k := range objectKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO foghorn.staging_cleanup_queue (object_key) VALUES ($1) ON CONFLICT (object_key) DO NOTHING
		`, k); err != nil {
			return err
		}
	}
	return nil
}

// reconstructAttemptObjectKeys returns an attempt's staging and version object keys, DERIVED deterministically
// from (asset_key, version=attempt_id, file_name) rather than the recorded version_key column — so an object
// promoted to S3 whose version_key was never recorded (a completion that died between promote and MarkVerified)
// is STILL reclaimable. Read within the given tx so it shares the guarded transition's snapshot. Enqueueing a
// key for an object that was never actually promoted is harmless: the S3 delete is a NotFound no-op.
func reconstructAttemptObjectKeys(ctx context.Context, tx *sql.Tx, attemptID string) (staging, version []string, err error) {
	rows, qErr := tx.QueryContext(ctx, `
		SELECT a.asset_key, a.version, o.file_name
		  FROM foghorn.thumbnail_task_object o
		  JOIN foghorn.thumbnail_task_assignment a ON a.attempt_id = o.attempt_id
		 WHERE o.attempt_id = $1
	`, attemptID)
	if qErr != nil {
		return nil, nil, qErr
	}
	defer rows.Close()
	for rows.Next() {
		var asset, ver, file string
		if sErr := rows.Scan(&asset, &ver, &file); sErr != nil {
			return nil, nil, sErr
		}
		staging = append(staging, ThumbnailStagingKey(asset, attemptID, file))
		if strings.TrimSpace(ver) != "" {
			version = append(version, ThumbnailVersionKey(asset, ver, file))
		}
	}
	return staging, version, rows.Err()
}

// ThumbnailDestinationClusters returns the DISTINCT official-durable destination clusters an asset's thumbnail
// attempts were published to. Thumbnails live on the tenant's official durable backend (destination_cluster),
// which is INDEPENDENT of where the parent artifact's own bytes live — so cleanup must route S3 deletion by this,
// never by the parent artifact's storage attribution, or a BYOC-origin artifact's platform-stored thumbnails
// would be deleted against the wrong (BYOC) cluster and leak. Read BEFORE the control rows are deleted.
func ThumbnailDestinationClusters(ctx context.Context, dbh *sql.DB, tenantID, assetKey string) ([]string, error) {
	if dbh == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" {
		return nil, nil
	}
	rows, err := dbh.QueryContext(ctx, `
		SELECT DISTINCT destination_cluster FROM foghorn.thumbnail_task_assignment
		 WHERE tenant_id = $1 AND asset_key = $2
	`, tenantID, assetKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if sErr := rows.Scan(&c); sErr != nil {
			return nil, sErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteThumbnailControlRows removes an asset's thumbnail control rows for one tenant — the active pointer and
// every publication attempt (each attempt's object rows cascade via the FK ON DELETE CASCADE). Called when the
// parent artifact is hard-purged so a deleted asset never strands its thumbnail pointer/assignment rows (nothing
// else keys on the now-gone artifact_hash). Both deletes run in ONE transaction (tenant-ownership-proved), so a
// racing publisher can never observe a half-deleted control state, and there is no window where the pointer is
// gone but the assignment remains (or vice versa). Idempotent (a re-run deletes zero rows). The S3 objects are
// freed separately by the caller via the artifact cleaner's thumbnail-prefix sweep.
func DeleteThumbnailControlRows(ctx context.Context, dbh *sql.DB, tenantID, assetKey string) error {
	if dbh == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" {
		return nil
	}
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	// Delete the pointer explicitly (keeps the tenant-ownership proof on it) then every attempt; the pointer FK
	// would also cascade it, but doing both atomically here removes any half-deleted window.
	if _, err := tx.ExecContext(ctx, `DELETE FROM foghorn.thumbnail_active_pointer WHERE tenant_id = $1 AND asset_key = $2`, tenantID, assetKey); err != nil {
		return err
	}
	// Cascades to foghorn.thumbnail_task_object.
	if _, err := tx.ExecContext(ctx, `DELETE FROM foghorn.thumbnail_task_assignment WHERE tenant_id = $1 AND asset_key = $2`, tenantID, assetKey); err != nil {
		return err
	}
	return tx.Commit()
}

// txObjectKeys returns the requested object-key columns for an attempt, read WITHIN the given transaction so the
// keys and the guarded transition share one consistent snapshot.
func txObjectKeys(ctx context.Context, tx *sql.Tx, attemptID, columns string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM foghorn.thumbnail_task_object WHERE attempt_id = $1`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	cols := strings.Count(columns, ",") + 1
	for rows.Next() {
		vals := make([]string, cols)
		ptrs := make([]interface{}, cols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		keys = append(keys, vals...)
	}
	return keys, rows.Err()
}

// queryAttemptIDs runs a single-column attempt_id query and returns the ids, closing rows via defer.
func queryAttemptIDs(ctx context.Context, dbh *sql.DB, query string, args ...interface{}) ([]string, error) {
	rows, err := dbh.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if sErr := rows.Scan(&id); sErr != nil {
			return nil, sErr
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RecoverStuckThumbnailAttempts is the crash-recovery reconciler pass (DB-only). It (1) RE-DRIVES every attempt
// left in 'publishing' — the idempotent pointer CAS completes a crash between promote and commit — and (2)
// FAILS + SWEEPS every attempt still in an earlier non-terminal state (assigned/uploading/verifying) past its
// lease expiry: its staging + any promoted version objects are enqueued for deletion and the attempt is marked
// 'failed'. A failed attempt never activated, so enqueueing its version objects can never delete a live one.
// Returns counts for observability (gced counts superseded versions reclaimed this pass) — the caller drains by
// re-running while a phase reports a full batch. `limit` bounds each phase per pass.
func RecoverStuckThumbnailAttempts(ctx context.Context, dbh *sql.DB, now time.Time, limit int) (redriven, failed, gced int, err error) {
	if dbh == nil {
		return 0, 0, 0, nil
	}
	if limit <= 0 {
		limit = 100
	}

	// Phase 1: re-drive NON-EXPIRED 'publishing' attempts (idempotent CAS completes a crash between promote and
	// commit). Expired 'publishing' attempts are left for phase 2 to fail + sweep, so an expired attempt can
	// never be published by the reconciler.
	publishing, qErr := queryAttemptIDs(ctx, dbh, `
		SELECT attempt_id FROM foghorn.thumbnail_task_assignment WHERE status = 'publishing' AND expiry > $1 LIMIT $2
	`, now, limit)
	if qErr != nil {
		return 0, 0, 0, qErr
	}
	for _, id := range publishing {
		if _, pErr := PublishThumbnailAttempt(ctx, dbh, id); pErr != nil {
			return redriven, failed, gced, pErr
		}
		redriven++
	}

	// Phase 2: fail + sweep every non-terminal attempt past its lease — including 'publishing' (a completion
	// that crashed after entering publishing but before committing the CAS).
	stuck, qErr2 := queryAttemptIDs(ctx, dbh, `
		SELECT attempt_id FROM foghorn.thumbnail_task_assignment
		 WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry < $1
		 LIMIT $2
	`, now, limit)
	if qErr2 != nil {
		return redriven, failed, gced, qErr2
	}
	for _, id := range stuck {
		didFail, fErr := failAndSweepThumbnailAttempt(ctx, dbh, id)
		if fErr != nil {
			return redriven, failed, gced, fErr
		}
		if didFail {
			failed++
		}
	}

	// Phase 3: GC superseded published versions. A published attempt whose version is no longer the asset's
	// active pointer is a stale backing set (its immutable objects still occupy storage). It is GC'd only after a
	// reader-safety horizon past its supersession (superseded_at) — Chandler serves a cached/last-known version
	// for up to thumbnailVersionTTL plus best-effort invalidation, so deleting a just-superseded version could
	// 404 an in-flight reader. The CURRENTLY active attempt is excluded (superseded_at IS NULL / version = active).
	supersededHorizon := now.Add(-thumbnailReaderSafetyHorizon)
	// Oldest-superseded-first so a backlog drains fairly rather than re-scanning the same arbitrary head; the
	// caller re-runs while gced == limit (a full batch) so accumulation from high-frequency live publication is
	// bounded rather than falling behind a single fixed batch per tick.
	superseded, qErr3 := queryAttemptIDs(ctx, dbh, `
		SELECT a.attempt_id
		  FROM foghorn.thumbnail_task_assignment a
		  JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key
		 WHERE a.status = 'published' AND a.version <> p.active_version
		   AND a.superseded_at IS NOT NULL AND a.superseded_at < $1
		 ORDER BY a.superseded_at ASC
		 LIMIT $2
	`, supersededHorizon, limit)
	if qErr3 != nil {
		return redriven, failed, gced, qErr3
	}
	for _, id := range superseded {
		if gcErr := gcSupersededThumbnailAttempt(ctx, dbh, id, supersededHorizon); gcErr != nil {
			return redriven, failed, gced, gcErr
		}
		gced++
	}
	return redriven, failed, gced, nil
}

// thumbnailReaderSafetyHorizon is how long a superseded version must remain before GC, so an in-flight Chandler
// still resolving it (cached/last-known up to thumbnailVersionTTL, plus best-effort invalidation) never 404s.
const thumbnailReaderSafetyHorizon = 5 * time.Minute

// StuckIncompleteThumbnailAttemptIDs returns non-expired attempts stuck in a PRE-publishing state whose last
// update is older than staleBefore — old enough that the node's completion is presumed LOST (a dropped
// ThumbnailUploaded, or a crash after the node uploaded but before the attempt reached 'publishing') rather than
// in-flight. The recovery reconciler re-drives each one through the idempotent completion so a one-shot VOD
// thumbnail is not orphaned. Excludes 'publishing' (recovery phase 1 re-drives those) and expired attempts
// (phase 2 fails + sweeps those).
func StuckIncompleteThumbnailAttemptIDs(ctx context.Context, dbh *sql.DB, now, staleBefore time.Time, limit int) ([]string, error) {
	if dbh == nil {
		return nil, nil
	}
	return queryAttemptIDs(ctx, dbh, `
		SELECT attempt_id FROM foghorn.thumbnail_task_assignment
		 WHERE status IN ('assigned', 'uploading', 'verifying')
		   AND expiry > $1 AND updated_at < $2
		 LIMIT $3
	`, now, staleBefore, limit)
}

// failAndSweepThumbnailAttempt atomically fails an abandoned attempt and enqueues its objects for cleanup. The
// guarded terminal transition runs FIRST; the enqueue happens in the SAME transaction ONLY if we won it. A
// concurrent completion that published the attempt makes the guarded UPDATE match zero rows, so a now-live
// attempt's objects are never queued for deletion.
func failAndSweepThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string) (bool, error) {
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	res, uErr := tx.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW()
		 WHERE attempt_id = $1 AND status IN ('assigned', 'uploading', 'verifying', 'publishing')
	`, attemptID)
	if uErr != nil {
		return false, uErr
	}
	if won, aErr := affectedOne(res); aErr != nil {
		return false, aErr
	} else if !won {
		return false, nil // a concurrent completion moved it on; leave its objects untouched
	}
	// Reconstruct staging + version keys deterministically (version = attempt_id), so a promoted-but-unrecorded
	// object — a completion that died between S3 promote and MarkVerified — is swept even though version_key was
	// never written to its row. This makes the reconciler's re-sweep claim true regardless of that ordering.
	staging, version, kErr := reconstructAttemptObjectKeys(ctx, tx, attemptID)
	if kErr != nil {
		return false, kErr
	}
	keys := append(staging, version...)
	if eErr := EnqueueThumbnailCleanup(ctx, tx, keys); eErr != nil {
		return false, eErr
	}
	if cErr := tx.Commit(); cErr != nil {
		return false, cErr
	}
	return true, nil
}

// gcSupersededThumbnailAttempt atomically deletes a superseded published attempt and enqueues its version
// objects. The guarded DELETE (still superseded past the horizon) runs FIRST; the enqueue happens in the SAME
// transaction ONLY if the DELETE won — so a version that (impossibly) became active again is never queued.
func gcSupersededThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string, supersededHorizon time.Time) error {
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	keys, kErr := txObjectKeys(ctx, tx, attemptID, "version_key")
	if kErr != nil {
		return kErr
	}
	res, dErr := tx.ExecContext(ctx, `
		DELETE FROM foghorn.thumbnail_task_assignment a
		 USING foghorn.thumbnail_active_pointer p
		 WHERE a.attempt_id = $1 AND p.asset_key = a.asset_key
		   AND a.version <> p.active_version
		   AND a.superseded_at IS NOT NULL AND a.superseded_at < $2
	`, attemptID, supersededHorizon)
	if dErr != nil {
		return dErr
	}
	if won, aErr := affectedOne(res); aErr != nil {
		return aErr
	} else if !won {
		return nil // no longer eligible (re-activated / already gone); enqueue nothing
	}
	if eErr := EnqueueThumbnailCleanup(ctx, tx, keys); eErr != nil {
		return eErr
	}
	return tx.Commit()
}

// ThumbnailResolveState is the serving decision for an asset_key: exactly one of these.
type ThumbnailResolveState int

const (
	// ThumbnailLegacyAllowed: no version published (and the parent is live/never-versioned) — serve the legacy
	// un-versioned object (migration fallback).
	ThumbnailLegacyAllowed ThumbnailResolveState = iota
	// ThumbnailActive: a version is published and the parent is not terminal — serve the versioned object.
	ThumbnailActive
	// ThumbnailGone: the parent artifact is TERMINAL (deleted/failed/expired/aborted) — the asset is gone; the
	// caller must serve NOTHING (evict any cache + 404/410), NOT the legacy key.
	ThumbnailGone
)

// ResolveThumbnailForServing is the canonical public/in-cell serving resolve for an asset_key (globally-unique
// stream_id UUID / opaque clip-dvr-vod hash). It returns a TRI-STATE so a terminal (deleted/failed/expired/
// aborted) parent produces a distinct GONE outcome instead of collapsing into the legacy fallback — otherwise a
// surviving legacy object would stay public after deletion. A live stream has no artifact row (never terminal).
func ResolveThumbnailForServing(ctx context.Context, dbh *sql.DB, assetKey string) (version string, state ThumbnailResolveState, err error) {
	if dbh == nil || strings.TrimSpace(assetKey) == "" {
		return "", ThumbnailLegacyAllowed, nil
	}
	var active sql.NullString
	var gone bool
	// Single read keyed by asset_key: the active version (if any) + whether the parent artifact is terminal.
	qErr := dbh.QueryRowContext(ctx, `
		SELECT p.active_version, COALESCE(a.status IN `+artifactTerminalStatusSQL+`, false) AS gone
		  FROM (SELECT $1::text AS k) k
		  LEFT JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = k.k
		  LEFT JOIN foghorn.artifacts a ON a.artifact_hash = k.k
	`, assetKey).Scan(&active, &gone)
	if qErr != nil {
		return "", ThumbnailLegacyAllowed, qErr
	}
	if gone {
		return "", ThumbnailGone, nil
	}
	if active.Valid && active.String != "" {
		return active.String, ThumbnailActive, nil
	}
	return "", ThumbnailLegacyAllowed, nil
}

// ResolveActiveThumbnailVersion is the simple form used where only the active version matters: ok=true with a
// version for an ACTIVE asset; ok=false for both LEGACY (serve legacy key) and GONE (a terminal parent). The
// serving path uses ResolveThumbnailForServing to distinguish GONE. Reads shared Postgres (any HA instance).
func ResolveActiveThumbnailVersion(ctx context.Context, dbh *sql.DB, assetKey string) (version string, ok bool, err error) {
	v, state, e := ResolveThumbnailForServing(ctx, dbh, assetKey)
	if e != nil {
		return "", false, e
	}
	return v, state == ThumbnailActive, nil
}
