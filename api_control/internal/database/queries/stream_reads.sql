-- name: CountStreamsForUser :one
SELECT COUNT(*)::integer
FROM commodore.streams
WHERE user_id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: CountStreamsForUserSearch :one
SELECT COUNT(*)::integer
FROM commodore.streams
WHERE user_id = $1 AND tenant_id = $2 AND deleted_at IS NULL
  AND (LOWER(title) LIKE $3 OR LOWER(internal_name) LIKE $3);

-- name: ListStreamsForward :many
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.user_id = sqlc.arg(user_id) AND s.tenant_id = sqlc.arg(tenant_id) AND s.deleted_at IS NULL
  AND (NOT sqlc.arg(apply_search)::boolean
       OR LOWER(s.title) LIKE sqlc.arg(search_like)
       OR LOWER(s.internal_name) LIKE sqlc.arg(search_like))
ORDER BY s.created_at DESC, s.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListStreamsForwardAfter :many
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.user_id = sqlc.arg(user_id) AND s.tenant_id = sqlc.arg(tenant_id) AND s.deleted_at IS NULL
  AND (NOT sqlc.arg(apply_search)::boolean
       OR LOWER(s.title) LIKE sqlc.arg(search_like)
       OR LOWER(s.internal_name) LIKE sqlc.arg(search_like))
  AND (s.created_at, s.id) < (sqlc.arg(cursor_time)::timestamp, sqlc.arg(cursor_id)::uuid)
ORDER BY s.created_at DESC, s.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListStreamsBackward :many
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.user_id = sqlc.arg(user_id) AND s.tenant_id = sqlc.arg(tenant_id) AND s.deleted_at IS NULL
  AND (NOT sqlc.arg(apply_search)::boolean
       OR LOWER(s.title) LIKE sqlc.arg(search_like)
       OR LOWER(s.internal_name) LIKE sqlc.arg(search_like))
ORDER BY s.created_at ASC, s.id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListStreamsBackwardBefore :many
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.user_id = sqlc.arg(user_id) AND s.tenant_id = sqlc.arg(tenant_id) AND s.deleted_at IS NULL
  AND (NOT sqlc.arg(apply_search)::boolean
       OR LOWER(s.title) LIKE sqlc.arg(search_like)
       OR LOWER(s.internal_name) LIKE sqlc.arg(search_like))
  AND (s.created_at, s.id) > (sqlc.arg(cursor_time)::timestamp, sqlc.arg(cursor_id)::uuid)
ORDER BY s.created_at ASC, s.id ASC
LIMIT sqlc.arg(row_limit);

-- name: GetStreamConfig :one
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.id = $1 AND s.user_id = $2 AND s.tenant_id = $3 AND s.deleted_at IS NULL;

-- name: GetStreamsConfigBatch :many
SELECT s.id, s.internal_name, s.stream_key, s.playback_id, s.title, s.description,
       s.is_recording_enabled, s.created_at, s.updated_at, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       s.active_ingest_cluster_id, s.dvr_chapter_mode, s.dvr_chapter_interval_seconds,
       s.dvr_retention_days_override, s.clip_retention_days_override, s.monitoring_enabled
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.id = ANY($1::uuid[]) AND s.user_id = $2 AND s.tenant_id = $3 AND s.deleted_at IS NULL;
