-- name: GetStreamAdmissionByKey :one
SELECT s.id, s.user_id, s.tenant_id, s.internal_name,
       u.is_active, s.is_recording_enabled, s.playback_id, s.ingest_mode
FROM commodore.streams s
JOIN commodore.users u ON s.user_id = u.id
WHERE s.stream_key = $1 AND s.deleted_at IS NULL;

-- name: AcquireIngestClaim :one
WITH held AS (
    SELECT current_stream.id,
           current_stream.active_ingest_cluster_id AS prev_cluster,
           current_stream.active_ingest_claim_id AS prev_token,
           COALESCE(
               current_stream.active_ingest_cluster_id IS NOT NULL
               AND current_stream.active_ingest_cluster_id <> ''
               AND current_stream.active_ingest_cluster_updated_at IS NOT NULL
               AND current_stream.active_ingest_cluster_updated_at >= NOW() - (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'),
               false
           )::boolean AS prev_fresh
    FROM commodore.streams current_stream
    WHERE current_stream.stream_key = sqlc.arg(lookup_stream_key)
      AND current_stream.deleted_at IS NULL
    FOR UPDATE
)
UPDATE commodore.streams s
SET active_ingest_cluster_id = sqlc.arg(cluster_id),
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = sqlc.arg(claim_token),
    updated_at = NOW()
FROM held
WHERE s.id = held.id
  AND (
      NOT held.prev_fresh
      OR (held.prev_cluster = sqlc.arg(cluster_id) AND held.prev_token = sqlc.arg(claim_token))
  )
RETURNING held.prev_cluster, held.prev_token, held.prev_fresh;

-- name: GetActiveIngestClaim :one
SELECT active_ingest_cluster_id, active_ingest_claim_id
FROM commodore.streams
WHERE stream_key = $1;

-- name: ResolveStreamContextByIdentifier :one
SELECT s.id, s.user_id, s.tenant_id, s.internal_name,
       u.is_active, s.is_recording_enabled, s.playback_id, s.ingest_mode,
       s.requires_auth, s.active_ingest_cluster_id,
       COALESCE(
           s.active_ingest_cluster_updated_at > NOW() - (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'),
           false
       )::boolean AS lease_fresh
FROM commodore.streams s
JOIN commodore.users u ON s.user_id = u.id
WHERE CASE sqlc.arg(identifier_kind)::text
    WHEN 'stream_id' THEN s.id::text = sqlc.arg(identifier_value)::text
    WHEN 'playback_id' THEN lower(s.playback_id::text) = lower(sqlc.arg(identifier_value)::text)
    WHEN 'internal_name' THEN s.internal_name = sqlc.arg(identifier_value)::text
    WHEN 'stream_key' THEN s.stream_key = sqlc.arg(identifier_value)::text
    ELSE false
END
AND (sqlc.arg(identifier_kind)::text <> 'stream_key' OR s.deleted_at IS NULL);
