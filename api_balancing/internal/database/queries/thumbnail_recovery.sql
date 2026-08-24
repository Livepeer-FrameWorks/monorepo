-- name: ClaimStuckIncompleteThumbnailAttempts :many
UPDATE foghorn.thumbnail_task_assignment q
SET recovery_leased_until = NOW() + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'),
    recovery_lease_token = gen_random_uuid()::text
WHERE q.attempt_id IN (
    SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a
    WHERE a.status IN ('assigned', 'uploading', 'verifying')
      AND a.expiry > NOW() AND a.updated_at < sqlc.arg(stale_before)
      AND (a.recovery_leased_until IS NULL OR a.recovery_leased_until <= NOW())
      AND (a.recovery_next_attempt_at IS NULL OR a.recovery_next_attempt_at <= NOW())
    ORDER BY COALESCE(a.recovery_next_attempt_at, a.updated_at) ASC
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING q.attempt_id, q.recovery_attempts, q.recovery_lease_token;

-- name: ClaimUnprojectedPublishedThumbnailAttempts :many
UPDATE foghorn.thumbnail_task_assignment q
SET recovery_leased_until = NOW() + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'),
    recovery_lease_token = gen_random_uuid()::text
WHERE q.attempt_id IN (
    SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a
    JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key AND p.active_version = a.attempt_id
    WHERE a.status = 'published' AND a.deterministic_projected_at IS NULL
      AND a.updated_at < sqlc.arg(stale_before)
      AND (a.recovery_leased_until IS NULL OR a.recovery_leased_until <= NOW())
      AND (a.recovery_next_attempt_at IS NULL OR a.recovery_next_attempt_at <= NOW())
    ORDER BY COALESCE(a.recovery_next_attempt_at, a.updated_at) ASC
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING q.attempt_id, q.recovery_attempts, q.recovery_lease_token;

-- name: ClaimDueReassertThumbnailAttempts :many
UPDATE foghorn.thumbnail_task_assignment q
SET recovery_leased_until = NOW() + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'),
    recovery_lease_token = gen_random_uuid()::text
WHERE q.attempt_id IN (
    SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a
    WHERE a.deterministic_reassert_at IS NOT NULL AND a.deterministic_reassert_at <= NOW()
      AND (a.recovery_leased_until IS NULL OR a.recovery_leased_until <= NOW())
      AND (a.recovery_next_attempt_at IS NULL OR a.recovery_next_attempt_at <= NOW())
    ORDER BY COALESCE(a.recovery_next_attempt_at, a.deterministic_reassert_at) ASC
    LIMIT sqlc.arg(row_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING q.attempt_id, q.recovery_attempts, q.recovery_lease_token;

-- name: UnprojectedThumbnailRecoveryBacklog :one
SELECT COUNT(*)::integer FROM foghorn.thumbnail_task_assignment a
JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key AND p.active_version = a.attempt_id
WHERE a.status = 'published' AND a.deterministic_projected_at IS NULL
  AND a.updated_at < $1
  AND (a.recovery_leased_until IS NULL OR a.recovery_leased_until <= NOW())
  AND (a.recovery_next_attempt_at IS NULL OR a.recovery_next_attempt_at <= NOW());

-- name: SettleThumbnailRecoveryDone :exec
UPDATE foghorn.thumbnail_task_assignment
SET recovery_leased_until = NULL, recovery_lease_token = NULL,
    recovery_attempts = 0, recovery_next_attempt_at = NULL, recovery_last_error = NULL
WHERE attempt_id = $1 AND recovery_lease_token = $2;

-- name: BackoffThumbnailRecovery :exec
UPDATE foghorn.thumbnail_task_assignment
SET recovery_attempts = recovery_attempts + 1,
    recovery_next_attempt_at = NOW() + (sqlc.arg(backoff_seconds)::bigint * INTERVAL '1 second'),
    recovery_leased_until = NULL, recovery_lease_token = NULL,
    recovery_last_error = sqlc.arg(error_message)::text
WHERE attempt_id = sqlc.arg(attempt_id) AND recovery_lease_token = sqlc.arg(lease_token);

-- name: ThumbnailRecoveryBacklog :one
SELECT COUNT(*)::integer FROM foghorn.thumbnail_task_assignment
WHERE status IN ('assigned', 'uploading', 'verifying')
  AND expiry > NOW() AND updated_at < $1
  AND (recovery_leased_until IS NULL OR recovery_leased_until <= NOW())
  AND (recovery_next_attempt_at IS NULL OR recovery_next_attempt_at <= NOW());
