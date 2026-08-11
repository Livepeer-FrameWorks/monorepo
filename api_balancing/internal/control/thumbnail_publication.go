package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Thumbnail crash-safe publication repo. Server-minted, node-bound attempts publish to per-attempt STAGING keys,
// get verified (provider-observed), promote to per-token candidate keys, then flip an active pointer — keyed by the
// globally-unique asset_key — via a guarded, monotonic CAS. The per-token candidate key (minted per completion;
// REQUIRED for every settlement; recorded as active_token) is PRIVATE to that completion, so a stale holder can only
// ever write its own candidate and never overwrite the winner's object. The winning candidate is then PROJECTED to the
// DETERMINISTIC served key (thumbnails/{asset}/{file}) that Chandler serves — the projection is fenced under the
// per-asset lock (see projectAndMarkThumbnail). active_version keeps the attempt id as the monotonic-CAS + GC anchor.
// tenant_id is carried as ownership/authorization attribution on every mutation, never as resource identity.

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

// ThumbnailVersionKey is the immutable PRIVATE candidate object for a version segment — the publication TOKEN (required
// on every settlement; the schema CHECK-constrains active_token present). Per-token keys are never overwritten across
// holders, so a stale attempt can never corrupt a live object; the active pointer alone decides which candidate is the
// winner, and that winner is PROJECTED to the deterministic served key `thumbnails/{asset}/{file}` that Chandler serves
// (the version key itself is internal, never served directly). The deterministic key doubles as the legacy fixed key
// for pre-state-machine rows.
func ThumbnailVersionKey(assetKey, version, file string) string {
	return "thumbnails/" + assetKey + "/v/" + version + "/" + file
}

// ThumbnailDeterministicKey is the STABLE served key Chandler serves at /assets/{asset}/{file}: thumbnails/{asset}/{file},
// with no version segment. The activated winner is PROJECTED here (copy from its private v/{token}/… candidate) and the
// projection is durably recovered (published-but-unprojected attempts are re-driven, then reasserted past the max-copy
// window), so it is not merely best-effort. Chandler serves this key directly with no version resolve. See
// docs/architecture/thumbnails.md.
func ThumbnailDeterministicKey(assetKey, file string) string {
	return "thumbnails/" + assetKey + "/" + file
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
// in ONE transaction, BEFORE the node holds any PUT URL. Objects are promoted to a per-token candidate key at
// publish time. Fail-closed on any incomplete identity or empty file set. Returns claimed=false on a reject.
//
// durable_backend_local is recorded TRUE — the write-time backend evidence (I2). This is a KNOWN value under the
// current single-S3-backend-per-cell scope, not an assumption: the mint (processThumbnailUploadRequest) drops
// StorageUnavailable AND StorageMintViaFederation BEFORE it ever claims, so an attempt only reaches this function
// for a StorageMintLocal destination — the bytes are on THIS cell's local S3. Persisting it atomically in the
// INSERT lets cleanup route the sweep local even when the destination cluster id differs (a locally-backed official
// alias), without reconstructing from current routing.
func ClaimThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID, tenantID, assetKey, nodeID, destinationCluster string, files []string, expiry time.Time) (claimed bool, err error) {
	if dbh == nil || attemptID == "" || tenantID == "" || assetKey == "" || nodeID == "" || destinationCluster == "" || len(files) == 0 {
		return false, nil
	}
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths

	// Per-asset fence FIRST: serialize with a concurrent stream-deletion (RecordStreamCleanupObligation) so the
	// tombstone check below cannot race an as-yet-uninserted tombstone row (a row lock can't fence a missing row).
	if lErr := lockThumbnailAsset(ctx, tx, assetKey); lErr != nil {
		return false, lErr
	}

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

	// TOMBSTONE FENCE at claim: a live stream (no artifacts row) that was deleted has a durable cleanup obligation
	// instead of a terminal artifact row. Never hand out upload authority for a tombstoned asset — otherwise the
	// node uploads staging garbage the cleanup sweep must chase. Locked FOR UPDATE to serialize against the delete.
	if tombstoned, tsErr := assetTombstonedTx(ctx, tx, assetKey); tsErr != nil {
		return false, tsErr
	} else if tombstoned {
		return false, nil
	}

	// Backend capture (RFC I2): record the fingerprint of THIS cell's local store ON the assignment. The bytes are
	// known-local here (see the durable_backend_local note above), so cleanup later compares this recorded id against
	// the cell's current store and fails closed on a mismatch (a forbidden repoint). FAIL CLOSED on an empty fingerprint:
	// a cell handing out thumbnail-upload authority must have a local store to attribute, and a fresh assignment must
	// never be written unattributed (which cleanup would later have to guess a store for).
	backendID := localBackendFingerprint()
	if backendID == "" {
		return false, fmt.Errorf("claim thumbnail attempt %s: no local backend fingerprint to attribute the assignment (no local S3 store) — refusing to mint upload authority", attemptID)
	}
	if _, execErr := tx.ExecContext(ctx, `
		INSERT INTO foghorn.thumbnail_task_assignment
			(attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry, durable_backend_local, backend_id)
		VALUES ($1, $2, $3, $4, $5, 'assigned', $1, $6, true, $7)
	`, attemptID, tenantID, assetKey, nodeID, destinationCluster, expiry, backendID); execErr != nil {
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
// MarkThumbnailObjectVerifiedToken records a verified object, CAS'ing the completion's publication lease token: a
// STALE holder (its lease expired and a peer re-acquired, minting a new token) matches zero rows — it can record
// verified objects only while it still owns the lease. The version_key it wrote is its OWN private candidate
// (`v/{token}/…`), so it can never overwrite the winner's object either way. The token is REQUIRED (a settlement
// without a held lease is fail-closed); production always holds one (the completion acquires it, recovery threads
// the persisted one).
func MarkThumbnailObjectVerifiedToken(ctx context.Context, dbh *sql.DB, attemptID, file, versionKey, etag string, size int64, token string) (bool, error) {
	if dbh == nil || token == "" {
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
		            AND a.expiry > NOW()
		            AND a.publish_lease_token = $6)
	`, attemptID, file, versionKey, etag, size, token)
	if err != nil {
		return false, err
	}
	return affectedOne(res)
}

// NOTE: the pre-publication status transitions (assigned → uploading → verifying, or → failed) are driven ONLY by
// tests, via a test-only helper (see thumbnail_transition_testonly_test.go). Production reaches 'publishing'/
// 'published' exclusively through the token-fenced EnterThumbnailPublishingToken / PublishThumbnailAttemptToken, so
// there is no exported generic-transition seam that could create publication state outside the token contract.

// artifactTerminalStatusSQL is the CANONICAL set of artifact statuses that permanently stop thumbnail publication
// AND resolution: the parent is gone or will never serve media. It mirrors the artifact state machine's terminal
// set used elsewhere (e.g. dvr_chapters_repo.go, the catalog trigger) — 'deleted'/'expired'/'aborted' are the
// gone states, 'failed' the no-media state. Keep this the single source used by claim/publish/resolve/cleanup.
const artifactTerminalStatusSQL = "('deleted', 'failed', 'expired', 'aborted')"

// parentArtifactTombstoned reports whether the asset an asset_key names must STOP thumbnail publication: either
// its parent artifact is in a TERMINAL state (see artifactTerminalStatusSQL), OR — for a live stream_id with no
// artifact row — a durable cleanup tombstone (stream_cleanup_obligation) exists. Used to fence promotion so a
// completion never publishes for a gone/dead parent (and never writes version objects into a prefix a purge or a
// stream-cleanup sweep is about to reclaim).
func parentArtifactTombstoned(ctx context.Context, dbh *sql.DB, assetKey string) (bool, error) {
	if dbh == nil || strings.TrimSpace(assetKey) == "" {
		return false, nil
	}
	var tombstoned bool
	err := dbh.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1), false)
		    OR EXISTS(SELECT 1 FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1)
	`, assetKey).Scan(&tombstoned)
	if err != nil {
		return false, err
	}
	return tombstoned, nil
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

// AcquireThumbnailPublishLease claims a token-fenced publication lease for an attempt and returns the holder
// TOKEN. It succeeds only when the attempt is non-terminal, UNEXPIRED (DB time), and NOT currently leased. The
// returned token is threaded through every subsequent settlement (MarkThumbnailObjectVerified,
// EnterThumbnailPublishing, PublishThumbnailAttempt), each of which CASes it — so a STALE holder whose lease
// expired and was re-acquired by a peer matches zero rows and cannot publish. The holder writes its promoted object
// to a per-token CANDIDATE key (v/{token}/…), so a stale holder can only ever overwrite its OWN candidate, never
// the winner's. The recovery fail-sweep honors the live lease. Returns token=="" (no error) when already leased.
func AcquireThumbnailPublishLease(ctx context.Context, dbh *sql.DB, attemptID string, leaseTTL time.Duration) (token string, err error) {
	if dbh == nil || attemptID == "" {
		return "", nil
	}
	var t sql.NullString
	scanErr := dbh.QueryRowContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment
		   SET publish_leased_until = NOW() + ($2 * INTERVAL '1 second'),
		       publish_lease_token = gen_random_uuid()::text
		 WHERE attempt_id = $1
		   AND status IN ('assigned', 'uploading', 'verifying', 'publishing')
		   AND expiry > NOW()
		   AND (publish_leased_until IS NULL OR publish_leased_until <= NOW())
		 RETURNING publish_lease_token
	`, attemptID, int64(leaseTTL.Seconds())).Scan(&t)
	if scanErr == sql.ErrNoRows {
		return "", nil // already leased by a live holder, or terminal/expired
	}
	if scanErr != nil {
		return "", scanErr
	}
	return t.String, nil
}

// ThumbnailCandidateVersion is the version segment served for a token-fenced publication: it IS the holder token,
// so each completion promotes to a private candidate key v/{token}/{file} that a stale holder can never share.
func ThumbnailCandidateVersion(token string) string { return token }

// EnterThumbnailPublishingToken moves an in-flight attempt (assigned/uploading/verifying) into 'publishing' — the
// durable state that gates the pointer CAS and blocks re-entry through the earlier states — CAS'ing the completion's
// lease token so a stale holder whose lease was re-acquired cannot advance it. Idempotent for an attempt already
// 'publishing' (matches zero rows); terminal attempts are not moved. The token is REQUIRED (fail-closed on empty).
func EnterThumbnailPublishingToken(ctx context.Context, dbh *sql.DB, attemptID, token string) (moved bool, err error) {
	if dbh == nil || token == "" {
		return false, nil
	}
	res, execErr := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment
		   SET status = 'publishing', updated_at = NOW()
		 WHERE attempt_id = $1 AND status IN ('assigned', 'uploading', 'verifying') AND expiry > NOW()
		   AND publish_lease_token = $2
	`, attemptID, token)
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
// PublishThumbnailAttemptToken is the token-fenced pointer switch. It (1) CASes the completion's lease token in the
// eligibility SELECT (REQUIRED, non-empty), so a stale holder whose lease was re-acquired cannot flip the pointer;
// (2) records the WINNING version's token as active_token (the winner is then projected to the DETERMINISTIC served
// key thumbnails/{asset}/{file}, which is what Chandler serves — active_token is the internal winner, not the served
// path); and (3) on a win DE-REGISTERS this winner's candidate objects from
// the cleanup queue (they were enqueued BEFORE promotion), so the now-live version is never deleted while a
// losing/stale completion's distinct per-token candidate stays queued and is reclaimed. In ONE transaction it also
// LOCKS + re-guards the attempt is 'publishing'/unexpired/verified/parent-not-tombstoned, flips the active pointer
// via a monotonic claim_seq CAS, marks 'published', and commits the durable side effects. A non-activating attempt
// is settled 'failed' with its objects enqueued so it never leaks. asset_key is the identity; tenant_id attribution.
func PublishThumbnailAttemptToken(ctx context.Context, dbh *sql.DB, attemptID, token string) (activated bool, err error) {
	if dbh == nil || attemptID == "" || token == "" {
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
		   AND publish_lease_token = $2
		 FOR UPDATE
	`, attemptID, token).Scan(&assetKey, &tenantID)
	if selErr == sql.ErrNoRows {
		return false, nil // not eligible: not publishing, expired, or already terminal
	}
	if selErr != nil {
		return false, selErr
	}

	// Per-asset fence: now that asset_key is known, take the shared advisory lock so this pointer flip serializes
	// with a concurrent stream-deletion for the same asset — the tombstone fence below then cannot race an
	// as-yet-uninserted tombstone row and flip the pointer for an already-deleted asset.
	if lErr := lockThumbnailAsset(ctx, tx, assetKey); lErr != nil {
		return false, lErr
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

	// TOMBSTONE FENCE (live streams): an asset with no artifacts row cannot be caught by the terminal-status fence
	// above, so a deleted LIVE stream is fenced here by its durable cleanup obligation. Locked FOR UPDATE so it
	// serializes against RecordStreamCleanupObligation. If tombstoned, settle this attempt failed (enqueue its
	// version objects for cleanup) rather than flip the pointer to a version the cleanup sweep is about to delete.
	if tombstoned, tsErr := assetTombstonedTx(ctx, tx, assetKey); tsErr != nil {
		return false, tsErr
	} else if tombstoned {
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
		INSERT INTO foghorn.thumbnail_active_pointer (asset_key, tenant_id, active_version, active_token, updated_at)
		VALUES ($2, $3, $1, $4, NOW())
		ON CONFLICT (asset_key) DO UPDATE
		   SET active_version = EXCLUDED.active_version, active_token = EXCLUDED.active_token,
		       tenant_id = EXCLUDED.tenant_id, updated_at = NOW()
		 WHERE foghorn.thumbnail_active_pointer.tenant_id = EXCLUDED.tenant_id
		   AND (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = EXCLUDED.active_version)
			 >= (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = foghorn.thumbnail_active_pointer.active_version)
	`, attemptID, assetKey, tenantID, token)
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
		// Enqueue the now-superseded staging objects (garbage once promoted) atomically with the pointer flip.
		// has_thumbnails is NOT flipped here: it is deferred to projectAndMarkThumbnail, which runs only AFTER the
		// winner's objects are projected to their deterministic served keys — so the API never advertises a thumbnail
		// Chandler cannot yet serve. A crash after the CAS but before projection leaves the attempt 'published' +
		// UNPROJECTED (deterministic_projected_at IS NULL); the recovery reconciler re-drives the projection and then
		// flips has_thumbnails, so the durable side effect is never permanently skipped.
		stagingKeys, sErr := txObjectKeys(ctx, tx, attemptID, "staging_key")
		if sErr != nil {
			return false, sErr
		}
		if eErr := EnqueueThumbnailCleanup(ctx, tx, stagingKeys); eErr != nil {
			return false, eErr
		}
		// De-register THIS winner's candidate objects (the completion enqueued them BEFORE promotion). Their keys
		// are the recorded version_key column (per-token `v/{token}/…`), now the LIVE served version — leaving them
		// queued would let the cleanup worker delete the live object. A losing/stale completion's candidate is a
		// DISTINCT per-token key that stays queued and is reclaimed; the prior winner's set is re-armed by the GC
		// after the reader-safety horizon via its recorded version_key, so nothing leaks.
		winnerKeys, wErr := txObjectKeys(ctx, tx, attemptID, "version_key")
		if wErr != nil {
			return false, wErr
		}
		if dErr := DequeueThumbnailCleanup(ctx, tx, winnerKeys); dErr != nil {
			return false, dErr
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

// projectionProviderAmbiguityWindow is the assumed maximum time an object store may still COMPLETE a copy it accepted
// AFTER the client context that issued it was cancelled — the irreducible ambiguity of a non-atomic remote copy. This
// 5-minute value is an EXPLICITLY ACCEPTED BETA ASSUMPTION, NOT a provider guarantee: Hetzner/R2 do not publish such a
// bound, so it is a deliberately conservative estimate (small-object copies complete in ms). It is the single knob to
// widen if late convergence is observed.
//
// Because the value is an assumption rather than a guarantee, the convergence it underwrites (the one-shot reassert and
// the delayed second sweep) is NOT strictly bounded: a straggler copy that completes LATER than this window is not
// corrected — it can overwrite the winner (a cosmetic thumbnail regression) or resurrect a deleted object (a
// revocation gap: Chandler serves the deterministic key regardless of control rows). This is the ACCEPTED, rare
// residual risk for the beta — it degrades a cosmetic thumbnail, never the durability of primary media.
const projectionProviderAmbiguityWindow = 5 * time.Minute

// thumbnailUploadTTL is how long a presigned staging-upload PUT stays valid after Foghorn issues it (see the mint
// site in ProcessThumbnailUpload). It is the OPERATION LIFETIME that bounds the deletion-straggler window below: an
// attempt that passed CLAIM just before the deletion tombstone can hold a valid PUT this long, so its object can still
// land (and then be promoted) up to this long AFTER the tombstone.
const thumbnailUploadTTL = 15 * time.Minute

// DeterministicCopyWindow bounds the two eventual-convergence mechanisms — the winner's one-shot reassert
// (deterministic_reassert_at) and the deletion's delayed second sweep + finalize (StreamCleanupJob). Both are anchored
// at an EVENT (the winner's settle, or the deletion tombstone), but a straggler object can be produced by an attempt
// that passed CLAIM BEFORE that event and then stays in flight. The bounding OPERATION LIFETIME is the presigned upload
// PUT (thumbnailUploadTTL, 15m) — the longest an object issued just before the anchor can still land — NOT the copy
// deadline (2m): a staging PUT presigned before the tombstone can land at +15m, and finalizing (deleting the control
// rows) before then would strand it. The store may then complete the abandoned promote up to
// projectionProviderAmbiguityWindow later, so the window is the SUM. A copy completing later than that assumption is
// the accepted residual risk documented on projectionProviderAmbiguityWindow.
const DeterministicCopyWindow = thumbnailUploadTTL + projectionProviderAmbiguityWindow

// projectAndMarkThumbnail projects a published winner's version objects to the DETERMINISTIC served key under an
// EVENTUAL-consistency contract. A PostgreSQL lock CANNOT make the copy strictly serial: the copy's DESTINATION write is
// unconditional (PromoteObject conditions only on the SOURCE ETag) and a copy the store has accepted can complete AFTER
// the client context is cancelled and any transaction released — so a loser's straggler can overwrite the winner. It
// therefore does NOT hold a lock across S3 I/O; it converges in three bounded steps:
//
//  1. CLAIM (short tx + per-asset lock): gate on not-tombstoned/terminal and this attempt still being the ACTIVE,
//     published, unprojected pointer. No S3 runs under the lock.
//  2. COPY (no tx, no lock): copy each version object to its deterministic served key.
//  3. SETTLE (short tx + per-asset lock): re-verify still-active + not-tombstoned/terminal, then stamp
//     deterministic_projected_at + expose has_thumbnails AND arm deterministic_reassert_at = NOW()+window. A superseded
//     loser that copied never settles (the CAS rejects it); its straggler overwrite is corrected by the current
//     winner's reassert (ReassertThumbnailProjection), which re-copies once past the window — for a straggler that
//     lands WITHIN the window. A copy completing later than the assumed provider tail is the accepted residual risk
//     (see projectionProviderAmbiguityWindow), not corrected by the one-shot reassert.
//
// Returns marked=true only when the settle committed. Idempotent; a failed/incomplete copy or a lost race commits
// nothing and leaves the attempt for recovery / the newer winner.
func projectAndMarkThumbnail(ctx context.Context, dbh *sql.DB, client S3ClientInterface, attemptID, assetKey, tenantID, servingCluster string, objs []ThumbnailObject, logger logging.Logger) (bool, error) {
	if dbh == nil || client == nil || attemptID == "" {
		return false, nil
	}
	// 1. CLAIM: gate this projection (no S3 under the lock).
	claimed, cErr := gateThumbnailProjection(ctx, dbh, attemptID, assetKey, false)
	if cErr != nil || !claimed {
		return false, cErr
	}
	// 2. COPY: outside any transaction / lock, under a context CAPPED at thumbnailCompletionDeadline. This cap bounds
	// only THIS copy operation, not DeterministicCopyWindow: the window is anchored on the presigned UPLOAD TTL
	// (thumbnailUploadTTL, the longest an object issued just before the anchor can still land) plus the provider tail,
	// NOT this 2m copy deadline — see DeterministicCopyWindow. A store that finishes an abandoned copy later than the
	// provider-tail assumption is the documented residual risk (see projectionProviderAmbiguityWindow).
	copyCtx, cancelCopy := context.WithTimeout(ctx, thumbnailCompletionDeadline)
	ok := copyThumbnailObjectsToDeterministic(copyCtx, client, assetKey, objs, logger)
	cancelCopy()
	if !ok {
		return false, nil
	}
	// 3. SETTLE: CAS-stamp projected + has_thumbnails + the serving cluster + arm the reassert clock.
	return settleThumbnailProjection(ctx, dbh, attemptID, assetKey, tenantID, servingCluster)
}

// gateThumbnailProjection is the CLAIM/SETTLE fence shared by projection and reassert. In a short transaction under the
// per-asset advisory lock it verifies the asset is not tombstoned (live-stream cleanup obligation) and not terminal
// (artifact, read FOR UPDATE so it serializes with a concurrent soft-delete/purge), then checks THIS attempt is still
// the ACTIVE pointer, 'published', and — unless allowProjected — not yet projected. It performs NO S3 I/O and holds no
// lock across a network call. Used as the pre-copy CLAIM (allowProjected=false → must be unprojected) and, by the
// reassert path, to decide whether the still-live winner should re-copy (allowProjected=true). Returns ok=false (no
// error) when the fence rejects it.
func gateThumbnailProjection(ctx context.Context, dbh *sql.DB, attemptID, assetKey string, allowProjected bool) (bool, error) {
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lErr := lockThumbnailAsset(ctx, tx, assetKey); lErr != nil {
		return false, lErr
	}
	if tombstoned, tErr := assetTombstonedTx(ctx, tx, assetKey); tErr != nil {
		return false, tErr
	} else if tombstoned {
		return false, nil
	}
	var artifactTerminal bool
	tErr := tx.QueryRowContext(ctx,
		`SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE`,
		assetKey).Scan(&artifactTerminal)
	if tErr != nil && tErr != sql.ErrNoRows {
		return false, tErr
	}
	if tErr == nil && artifactTerminal {
		return false, nil
	}
	// Active-pointer check: only the CURRENT winner may write the shared deterministic key. When claiming for the
	// initial projection we additionally require it to be unprojected; the reassert re-copies an already-projected winner.
	unprojectedClause := ""
	if !allowProjected {
		unprojectedClause = " AND a.deterministic_projected_at IS NULL"
	}
	var ok bool
	if aErr := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM foghorn.thumbnail_task_assignment a
			  JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key AND p.active_version = a.attempt_id
			 WHERE a.attempt_id = $1 AND a.status = 'published'`+unprojectedClause+`
		)`, attemptID).Scan(&ok); aErr != nil {
		return false, aErr
	}
	return ok, tx.Commit()
}

// settleThumbnailProjection is step 3 of projection: after the copy landed, re-verify (under the per-asset lock) that
// this attempt is STILL the active, published, unprojected winner and not tombstoned/terminal, then stamp
// deterministic_projected_at, arm the one-shot reassert clock (deterministic_reassert_at = NOW()+window), and expose
// has_thumbnails + the AUTHORITATIVE thumbnail_serving_cluster_id (the winning assignment's official-durable destination
// cluster, so the catalog links the correct Chandler even for a BYOC/cross-cell artifact). A superseded loser fails the
// CAS here (marked=false) so it never advertises stale bytes; its straggler overwrite is corrected by the current
// winner's reassert when it lands within the copy window (a later straggler is the accepted residual risk on
// projectionProviderAmbiguityWindow). Idempotent. servingCluster is write-once and rides the has_thumbnails catalog-revision bump so it
// projects to Commodore on the same snapshot (no trigger change needed).
func settleThumbnailProjection(ctx context.Context, dbh *sql.DB, attemptID, assetKey, tenantID, servingCluster string) (bool, error) {
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	if lErr := lockThumbnailAsset(ctx, tx, assetKey); lErr != nil {
		return false, lErr
	}
	if tombstoned, tErr := assetTombstonedTx(ctx, tx, assetKey); tErr != nil {
		return false, tErr
	} else if tombstoned {
		return false, nil
	}
	var artifactTerminal bool
	tErr := tx.QueryRowContext(ctx,
		`SELECT status IN `+artifactTerminalStatusSQL+` FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE`,
		assetKey).Scan(&artifactTerminal)
	if tErr != nil && tErr != sql.ErrNoRows {
		return false, tErr
	}
	if tErr == nil && artifactTerminal {
		return false, nil
	}
	res, uErr := tx.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment a
		   SET deterministic_projected_at = NOW(), deterministic_reassert_at = NOW() + ($3 * INTERVAL '1 second')
		 WHERE a.attempt_id = $1 AND a.status = 'published' AND a.deterministic_projected_at IS NULL
		   AND EXISTS (SELECT 1 FROM foghorn.thumbnail_active_pointer p WHERE p.asset_key = $2 AND p.active_version = $1)
	`, attemptID, assetKey, int64(DeterministicCopyWindow.Seconds()))
	if uErr != nil {
		return false, uErr
	}
	marked, mErr := affectedOne(res)
	if mErr != nil {
		return false, mErr
	}
	if marked {
		// Flip has_thumbnails AND stamp the authoritative serving cluster in ONE write (a no-op for a live stream_id
		// with no artifact row). NULLIF('' ) keeps an empty destination from clobbering a set value; the WHERE fires on
		// the has_thumbnails flip (which bumps catalog_revision → re-projects, carrying the serving cluster) or when a
		// non-empty serving cluster first differs.
		if _, hErr := tx.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			   SET has_thumbnails = true,
			       thumbnail_serving_cluster_id = COALESCE(NULLIF($3, ''), thumbnail_serving_cluster_id),
			       updated_at = NOW()
			 WHERE artifact_hash = $1 AND tenant_id::text = $2
			   AND (has_thumbnails IS DISTINCT FROM true
			        OR ($3 <> '' AND thumbnail_serving_cluster_id IS DISTINCT FROM $3))
		`, assetKey, tenantID, servingCluster); hErr != nil {
			return false, hErr
		}
	}
	return marked, tx.Commit()
}

// clearThumbnailReassert clears the one-shot reassert clock (deterministic_reassert_at = NULL) for an attempt whose
// reassert pass completed (re-copied, or skipped because superseded/gone). Idempotent.
func clearThumbnailReassert(ctx context.Context, dbh *sql.DB, attemptID string) error {
	if dbh == nil {
		return nil
	}
	_, err := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment SET deterministic_reassert_at = NULL WHERE attempt_id = $1
	`, attemptID)
	return err
}

// sqlExecer is satisfied by both *sql.DB and *sql.Tx, so cleanup enqueue can run inside a transaction that
// ALSO performs the guarded terminal transition — the two must be atomic (see RecoverStuckThumbnailAttempts).
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// EnqueueThumbnailCleanup durably enqueues object keys for S3 deletion by the shared staging-cleanup worker.
// Idempotent, and RE-ARMS on conflict (ON CONFLICT DO UPDATE, see below); empty keys are skipped. Used for staged
// objects (garbage once promoted) and for the version objects of an abandoned/superseded attempt. MUST run in the
// SAME transaction as the guarded status change that authorizes the deletion — never before it — or a concurrent
// completion that activates the version leaves a live object queued for deletion.
func EnqueueThumbnailCleanup(ctx context.Context, ex sqlExecer, objectKeys []string) error {
	if ex == nil {
		return nil
	}
	// Attribute the staging garbage to THIS cell's store so the cleanup worker resolves a recorded owner, never a
	// guessed current store. FAIL CLOSED on an empty fingerprint: a cell minting these objects has a local store.
	backendID := localBackendFingerprint()
	if backendID == "" && len(objectKeys) > 0 {
		return fmt.Errorf("enqueue thumbnail cleanup: no local backend fingerprint to attribute staging garbage")
	}
	for _, k := range objectKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		// RE-ARM on conflict, do NOT no-op: a re-enqueue must invalidate any IN-FLIGHT cleanup of this key.
		// Otherwise this race leaks: a worker claims the row, deletes the (still-absent) object, a completion then
		// promotes the object AND re-enqueues it (which would collapse via DO NOTHING), and the worker's
		// token-fenced settlement removes the row — orphaning the just-promoted object. Clearing leased_until +
		// lease_token makes the in-flight worker's settlement (WHERE lease_token = its token) a no-op, and
		// next_attempt_at = NOW() re-schedules the row so a fresh claim re-deletes the now-present object.
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO foghorn.staging_cleanup_queue (object_key, backend_id) VALUES ($1, $2)
			ON CONFLICT (object_key) DO UPDATE
			  SET next_attempt_at = NOW(), leased_until = NULL, lease_token = NULL,
			      backend_id = COALESCE(foghorn.staging_cleanup_queue.backend_id, EXCLUDED.backend_id)
		`, k, backendID); err != nil {
			return err
		}
	}
	return nil
}

// thumbnailCandidateCleanupGrace delays a PRE-promotion candidate's cleanup so the shared worker does not, in the
// common case, claim the queue row BEFORE the object exists (a Delete of the still-absent key is a NotFound no-op
// that would drop the row and orphan the object once promotion creates it). The grace exceeds a completion's bounded
// deadline, so a WINNER dequeues its live candidate well before the grace elapses; a losing/crashed holder leaves
// the row to fire after the grace, by which time the object exists and is reclaimed. NOTE: this is time-based, not a
// hard fence — an object-store copy that lands after a cancelled context could (rarely) materialize outside this
// window. Because the candidate key is per-token (private to one completion), that can only ever leak an orphan of
// an abandoned attempt, never corrupt the live winner's object; the asset-deletion prefix sweep is the final backstop.
const thumbnailCandidateCleanupGrace = 15 * time.Minute

// thumbnailCompletionDeadline bounds a completion's S3 verify/promote/settle work so a stuck op (callers pass
// context.Background()) is aborted and left for recovery rather than running unbounded. It MUST stay strictly less
// than thumbnailCandidateCleanupGrace so the winner's dequeue normally precedes the grace firing.
const thumbnailCompletionDeadline = 2 * time.Minute

// EnqueueThumbnailCleanupDeferred records a cleanup obligation that is NOT immediately due (next_attempt_at = NOW()
// + delay). Used for the pre-promotion candidate enqueue so the object is guaranteed to exist before the worker can
// act on the key. On conflict it pushes the due time no EARLIER (GREATEST) and clears any lease so an in-flight
// worker's settlement no-ops. Empty keys are skipped.
func EnqueueThumbnailCleanupDeferred(ctx context.Context, ex sqlExecer, objectKeys []string, delay time.Duration) error {
	if ex == nil {
		return nil
	}
	secs := int64(delay.Seconds())
	backendID := localBackendFingerprint()
	if backendID == "" && len(objectKeys) > 0 {
		return fmt.Errorf("enqueue deferred thumbnail cleanup: no local backend fingerprint to attribute staging garbage")
	}
	for _, k := range objectKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO foghorn.staging_cleanup_queue (object_key, next_attempt_at, backend_id)
			VALUES ($1, NOW() + ($2 * INTERVAL '1 second'), $3)
			ON CONFLICT (object_key) DO UPDATE
			  SET next_attempt_at = GREATEST(foghorn.staging_cleanup_queue.next_attempt_at, EXCLUDED.next_attempt_at),
			      leased_until = NULL, lease_token = NULL,
			      backend_id = COALESCE(foghorn.staging_cleanup_queue.backend_id, EXCLUDED.backend_id)
		`, k, secs, backendID); err != nil {
			return err
		}
	}
	return nil
}

// DequeueThumbnailCleanup removes object keys from the staging-cleanup queue — used by the publish CAS to
// de-register the winner's candidate objects (enqueued before promotion) once they become the live version, so the
// cleanup worker never deletes a live object. MUST run in the SAME transaction as the guarded pointer flip that
// makes those objects live. A stale/losing completion's per-token candidate is a distinct key and is NOT removed.
func DequeueThumbnailCleanup(ctx context.Context, ex sqlExecer, objectKeys []string) error {
	if ex == nil {
		return nil
	}
	for _, k := range objectKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if _, err := ex.ExecContext(ctx, `DELETE FROM foghorn.staging_cleanup_queue WHERE object_key = $1`, k); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueThumbnailVersionOrphansIfDead enqueues an attempt's promoted VERSION objects for cleanup when this attempt
// can no longer become the live version — either the asset is GONE (terminal artifact parent OR a stream cleanup
// tombstone), OR this attempt itself is TERMINAL-'failed' (e.g. the recovery reconciler expired + swept it while a
// slow completion was still promoting). A completion calls this after it promoted objects but could not publish:
// the promoted objects (deterministic version keys) would otherwise leak, because the assignment may already be
// deleted (so the recovery fail-sweep can't reconstruct them) or already failed (so recovery's guarded sweep
// matched zero rows and enqueued nothing before the object existed).
//
// The gate is essential: it must NOT fire when the attempt is still live/retryable or when a concurrent completion
// PUBLISHED it — deleting a live version key would drop a served object. It fires only for a dead attempt (failed)
// or a gone asset, where the promoted key can never be the active version. The objects exist (the completion just
// promoted them), so the shared StagingCleanupJob deletes them; keys for un-promoted files are NotFound no-ops.
// Returns whether it enqueued.
func EnqueueThumbnailVersionOrphansIfDead(ctx context.Context, dbh *sql.DB, attemptID, assetKey, version string, files []string) (bool, error) {
	if dbh == nil || strings.TrimSpace(assetKey) == "" {
		return false, nil
	}
	gone, err := parentArtifactTombstoned(ctx, dbh, assetKey)
	if err != nil {
		return false, err
	}
	if !gone {
		// Asset still live — enqueue only if THIS attempt is terminally failed (can never publish its version).
		var failed bool
		if sErr := dbh.QueryRowContext(ctx,
			`SELECT status = 'failed' FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1`, attemptID).Scan(&failed); sErr == sql.ErrNoRows {
			// Assignment already deleted (racing stream cleanup) → the version can never publish → treat as dead.
			failed = true
		} else if sErr != nil {
			return false, sErr
		}
		if !failed {
			return false, nil // live/retryable attempt — leave the objects for a legitimate re-drive
		}
	}
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, ThumbnailVersionKey(assetKey, version, f))
	}
	return true, EnqueueThumbnailCleanup(ctx, dbh, keys)
}

// reconstructAttemptObjectKeys returns an attempt's staging and version object keys, DERIVED deterministically
// from (asset_key, version-segment, file_name) rather than the recorded version_key column — so an object promoted
// to S3 whose version_key was never recorded (a completion that died between promote and MarkVerified) is STILL
// reclaimable. The version segment is the completion's per-token candidate segment (publish_lease_token), falling
// back to a.version (attempt_id) for a never-leased row — matching exactly what the completion promotes to. Read
// within the given tx so it shares the guarded transition's snapshot. Enqueueing a key for an object that was never
// actually promoted is harmless: the S3 delete is a NotFound no-op.
func reconstructAttemptObjectKeys(ctx context.Context, tx *sql.Tx, attemptID string) (staging, version []string, err error) {
	rows, qErr := tx.QueryContext(ctx, `
		SELECT a.asset_key, COALESCE(NULLIF(a.publish_lease_token,''), a.version), o.file_name
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

// ThumbnailDestination is one distinct (destination cluster, backend_id) an asset's thumbnails were published to,
// plus the write-time backend evidence for it: BackendLocal true means those bytes are on THIS cell's local S3 (a
// locally-backed official alias), so cleanup deletes locally regardless of the cluster id. BackendID is the recorded
// physical store the bytes were written to; under the immutable-backend model cleanup deletes from that store when it
// is the cell's current one and fails closed on a mismatch, rather than resolving an arbitrary backend's adapter.
type ThumbnailDestination struct {
	Cluster      string
	BackendLocal bool
	BackendID    string
}

// ThumbnailDestinationClusters returns the DISTINCT (official-durable destination cluster, recorded backend_id) an
// asset's thumbnail attempts were published to, each with its recorded backend-local fact (bool_or over the group).
// Thumbnails live on the tenant's official durable backend (destination_cluster), which is INDEPENDENT of where
// the parent artifact's own bytes live — so cleanup must route S3 deletion by this + the recorded backend fact,
// never by the parent artifact's storage attribution or a bare cluster-id compare. Grouping by backend_id too means
// thumbnails that span a repoint (attempts on two physical stores) each get swept from the store they live on. Read
// BEFORE control rows go.
func ThumbnailDestinationClusters(ctx context.Context, dbh *sql.DB, tenantID, assetKey string) ([]ThumbnailDestination, error) {
	if dbh == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" {
		return nil, nil
	}
	rows, err := dbh.QueryContext(ctx, `
		SELECT destination_cluster, COALESCE(backend_id, ''), bool_or(durable_backend_local) FROM foghorn.thumbnail_task_assignment
		 WHERE tenant_id = $1 AND asset_key = $2
		 GROUP BY destination_cluster, backend_id
	`, tenantID, assetKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThumbnailDestination
	for rows.Next() {
		var d ThumbnailDestination
		if sErr := rows.Scan(&d.Cluster, &d.BackendID, &d.BackendLocal); sErr != nil {
			return nil, sErr
		}
		out = append(out, d)
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
	if err := DeleteThumbnailControlRowsTx(ctx, tx, tenantID, assetKey); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteThumbnailControlRowsTx does the control-row deletion in the CALLER's transaction, so it can be composed
// ATOMICALLY with other work (e.g. stream-cleanup's final-target reclaim + parent settlement, which must not leave a
// pending parent with zero targets if control-row cleanup or settlement fails). Same body as DeleteThumbnailControlRows
// minus begin/commit.
func DeleteThumbnailControlRowsTx(ctx context.Context, tx *sql.Tx, tenantID, assetKey string) error {
	if tx == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(assetKey) == "" {
		return nil
	}
	// BEFORE deleting the assignment rows, ENQUEUE the reconstructable candidate + staging keys for EVERY attempt of
	// this asset (candidate segment = the attempt's publish token, staging = attempt_id — both derivable from the
	// still-present rows). This closes the promote-vs-delete leak: a completion can promote an object AFTER the
	// drainer's prefix-list snapshot but before its row is deleted; because the keys are reconstructable here while
	// the rows still exist, the shared StagingCleanupJob deletes them even though the prefix sweep missed them and
	// the assignment is gone. The asset is being deleted, so no future completion will publish these keys — a key
	// whose object was never promoted is a NotFound no-op. Same idempotent queue the recovery fail-sweep uses.
	if kErr := enqueueAssetThumbnailObjectKeys(ctx, tx, tenantID, assetKey); kErr != nil {
		return kErr
	}
	// Delete the pointer explicitly (keeps the tenant-ownership proof on it) then every attempt; the pointer FK
	// would also cascade it, but doing both atomically here removes any half-deleted window.
	if _, err := tx.ExecContext(ctx, `DELETE FROM foghorn.thumbnail_active_pointer WHERE tenant_id = $1 AND asset_key = $2`, tenantID, assetKey); err != nil {
		return err
	}
	// Cascades to foghorn.thumbnail_task_object.
	if _, err := tx.ExecContext(ctx, `DELETE FROM foghorn.thumbnail_task_assignment WHERE tenant_id = $1 AND asset_key = $2`, tenantID, assetKey); err != nil {
		return err
	}
	return nil
}

// enqueueAssetThumbnailObjectKeys reconstructs the staging + candidate object keys for EVERY attempt of an asset and
// enqueues them for the shared staging-cleanup worker, in the caller's transaction. Used right before the asset's
// control rows are deleted so a late-promoted object (whose row is about to vanish) is still swept. The candidate
// segment is the attempt's publish token (`COALESCE(publish_lease_token, version)`) — matching exactly what a
// completion promotes to (`v/{token}/…`) — plus the recorded version_key column for a completion whose token was
// re-minted by a peer since (so a prior holder's candidate is still reclaimed). A key whose object was never
// promoted is a NotFound no-op. This is a backstop; the authoritative removal is the caller's `thumbnails/{hash}/`
// prefix delete.
func enqueueAssetThumbnailObjectKeys(ctx context.Context, tx *sql.Tx, tenantID, assetKey string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.attempt_id, COALESCE(NULLIF(a.publish_lease_token,''), a.version), COALESCE(o.version_key,''), o.file_name
		  FROM foghorn.thumbnail_task_assignment a
		  JOIN foghorn.thumbnail_task_object o ON o.attempt_id = a.attempt_id
		 WHERE a.tenant_id = $1 AND a.asset_key = $2
	`, tenantID, assetKey)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var attempt, version, recordedVersionKey, file string
		if sErr := rows.Scan(&attempt, &version, &recordedVersionKey, &file); sErr != nil {
			rows.Close() //nolint:errcheck,sqlclosecheck
			return sErr
		}
		keys = append(keys, ThumbnailStagingKey(assetKey, attempt, file))
		if strings.TrimSpace(version) != "" {
			keys = append(keys, ThumbnailVersionKey(assetKey, version, file))
		}
		if k := strings.TrimSpace(recordedVersionKey); k != "" {
			keys = append(keys, k)
		}
	}
	if rErr := rows.Err(); rErr != nil {
		rows.Close() //nolint:errcheck,sqlclosecheck
		return rErr
	}
	rows.Close() //nolint:errcheck,sqlclosecheck
	return EnqueueThumbnailCleanup(ctx, tx, keys)
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

	// EXPIRY is compared against DATABASE NOW() (not the caller's wall clock): the lease boundary must be owned by
	// the DB so a replica with a fast clock cannot fail an attempt the DB still considers live, and so a
	// selection-vs-settlement race cannot straddle the boundary. (The GC horizon in phase 3 stays caller-derived —
	// it is a coarse reader-safety delay, not a lease.)
	//
	// Phase 1: re-drive NON-EXPIRED 'publishing' attempts (idempotent CAS completes a crash between promote and
	// commit). Expired 'publishing' attempts are left for phase 2 to fail + sweep, so an expired attempt can
	// never be published by the reconciler.
	// Read the attempt id AND its PERSISTED publication token: a production 'publishing' row was fenced under a
	// token, so the re-drive MUST carry that same token (the no-token wrapper would match zero rows and silently
	// leave the attempt to expire+fail — a lost thumbnail). The objects were promoted to v/{token}/…, so re-driving
	// under the persisted token also serves the objects that actually exist.
	pubRows, qErr := dbh.QueryContext(ctx, `
		SELECT attempt_id, COALESCE(publish_lease_token, '') FROM foghorn.thumbnail_task_assignment
		 WHERE status = 'publishing' AND expiry > NOW() LIMIT $1
	`, limit)
	if qErr != nil {
		return 0, 0, 0, qErr
	}
	type publishingAttempt struct{ id, token string }
	var publishing []publishingAttempt
	for pubRows.Next() {
		var pa publishingAttempt
		if sErr := pubRows.Scan(&pa.id, &pa.token); sErr != nil {
			pubRows.Close() //nolint:errcheck,sqlclosecheck
			return 0, 0, 0, sErr
		}
		publishing = append(publishing, pa)
	}
	if rErr := pubRows.Err(); rErr != nil {
		pubRows.Close() //nolint:errcheck,sqlclosecheck
		return 0, 0, 0, rErr
	}
	pubRows.Close() //nolint:errcheck,sqlclosecheck
	for _, pa := range publishing {
		if _, pErr := PublishThumbnailAttemptToken(ctx, dbh, pa.id, pa.token); pErr != nil {
			return redriven, failed, gced, pErr
		}
		redriven++
	}

	// Phase 2: fail + sweep every non-terminal attempt past its lease — including 'publishing' (a completion
	// that crashed after entering publishing but before committing the CAS). expiry < NOW() (DB-owned boundary).
	stuck, qErr2 := queryAttemptIDs(ctx, dbh, `
		SELECT attempt_id FROM foghorn.thumbnail_task_assignment
		 WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry < NOW()
		 LIMIT $1
	`, limit)
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

	// Phase 3: GC superseded published versions. A published attempt whose version is no longer the asset's active
	// pointer is a stale internal backing set (per-token version-key objects still occupy storage). Version keys are
	// no longer served directly — Chandler serves the DETERMINISTIC key (a copy of the active version) at /assets —
	// so this is pure internal storage reclamation. It is still deferred by a reader-safety horizon past supersession
	// (superseded_at) as conservative retention. The CURRENTLY active attempt is excluded (superseded_at IS NULL /
	// version = active); its version key is the source of the live deterministic copy.
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

	// Re-driving published-but-UNPROJECTED attempts (a crash between the publish CAS and the deterministic copy, or an
	// S3 failure that outlasted the inline retry) is NOT done here: it does S3 work whose success is data-dependent
	// (the source object may still be absent, the copy may keep failing), so a plain oldest-N drain would re-select the
	// same poison rows every pass, count non-progress as progress, and starve newer projections. It runs in the LEASED
	// recovery phase instead (ClaimUnprojectedPublishedThumbnailAttempts + ReprojectPublishedThumbnailAttempt), which
	// backs off non-progressing rows and counts only real progress.
	return redriven, failed, gced, nil
}

// thumbnailReaderSafetyHorizon is how long a superseded version key must remain before GC. The served object is the
// DETERMINISTIC key (a copy of the current winner), so a reader never depends on a superseded version key directly; the
// horizon is conservative retention covering an in-flight projection re-drive that still reads the recorded version key.
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

// failAndSweepThumbnailAttempt atomically fails an EXPIRED abandoned attempt and enqueues its objects for cleanup.
// The guarded terminal transition runs FIRST; the enqueue happens in the SAME transaction ONLY if we won it. A
// concurrent completion that published the attempt makes the guarded UPDATE match zero rows, so a now-live
// attempt's objects are never queued for deletion. The UPDATE re-checks `expiry <= NOW()` (DB-owned lease
// boundary) so a still-live attempt selected under a fast replica clock is NOT failed out from under a completion
// that is legitimately promoting it.
func failAndSweepThumbnailAttempt(ctx context.Context, dbh *sql.DB, attemptID string) (bool, error) {
	tx, txErr := dbh.BeginTx(ctx, nil)
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	res, uErr := tx.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW()
		 WHERE attempt_id = $1 AND status IN ('assigned', 'uploading', 'verifying', 'publishing')
		   AND expiry <= NOW()
		   AND (publish_leased_until IS NULL OR publish_leased_until <= NOW())
	`, attemptID)
	if uErr != nil {
		return false, uErr
	}
	if won, aErr := affectedOne(res); aErr != nil {
		return false, aErr
	} else if !won {
		return false, nil // a concurrent completion moved it on; leave its objects untouched
	}
	// Reconstruct staging + candidate keys from the attempt's own segments (staging = attempt_id, candidate =
	// COALESCE(publish_lease_token, version)), so a promoted-but-unrecorded object — a completion that died between
	// S3 promote and MarkVerified — is swept even though version_key was never written to its row.
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
