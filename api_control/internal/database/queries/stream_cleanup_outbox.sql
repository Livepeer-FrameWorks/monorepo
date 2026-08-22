-- name: EnqueueStreamCleanup :one
INSERT INTO commodore.stream_cleanup_outbox (stream_id, tenant_id)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(tenant_id)::uuid)
ON CONFLICT (stream_id) DO NOTHING
RETURNING stream_id::text;

-- name: FailStreamCleanup :exec
UPDATE commodore.stream_cleanup_outbox
SET attempts = sqlc.arg(attempts),
    next_attempt_at = NOW() + (sqlc.arg(backoff_ms)::bigint * INTERVAL '1 millisecond'),
    last_error = sqlc.arg(last_error)::text
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND status = 'pending'
  AND (sqlc.arg(lease_token)::text = '' OR lease_token = sqlc.arg(lease_token)::text)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: MarkStreamThumbnailCleanupAcked :exec
UPDATE commodore.stream_cleanup_outbox
SET thumbnail_cleanup_acked_at = NOW()
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND thumbnail_cleanup_acked_at IS NULL
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListStreamCleanupClips :many
SELECT clip_hash, COALESCE(origin_cluster_id::text, ''::text)::text AS origin_cluster_id
FROM commodore.clips
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListStreamCleanupDVRs :many
SELECT dvr_hash, COALESCE(origin_cluster_id::text, ''::text)::text AS origin_cluster_id
FROM commodore.dvr_recordings
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetStreamThumbnailServingCells :one
SELECT COALESCE(thumbnail_serving_cluster_ids, '{}') AS thumbnail_serving_cluster_ids
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ClaimStreamCleanupBatch :many
SELECT stream_id::text AS stream_id,
       tenant_id::text AS tenant_id,
       attempts,
       CASE WHEN thumbnail_cleanup_acked_at IS NULL THEN false ELSE true END::boolean AS thumbnail_cleanup_acked
FROM commodore.stream_cleanup_outbox
WHERE status = 'pending'
  AND next_attempt_at <= NOW()
ORDER BY next_attempt_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size);

-- name: LeaseStreamCleanup :one
UPDATE commodore.stream_cleanup_outbox
SET next_attempt_at = NOW() + (sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond'),
    lease_token = gen_random_uuid()::text
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND status = 'pending'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING lease_token;

-- name: SettleStreamCleanupForFinalization :one
UPDATE commodore.stream_cleanup_outbox
SET status = 'completed', completed_at = NOW(), last_error = NULL
WHERE stream_id = sqlc.arg(stream_id)::uuid
  AND status = 'pending'
  AND (sqlc.arg(lease_token)::text = '' OR lease_token = sqlc.arg(lease_token)::text)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING tenant_id::text;

-- name: GetStreamFinalizationUser :one
SELECT COALESCE(user_id::text, ''::text)::text AS user_id
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: HardDeleteFinalizedStream :execrows
DELETE FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND deleted_at IS NOT NULL;
