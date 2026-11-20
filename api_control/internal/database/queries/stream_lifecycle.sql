-- name: MarkCreatedStreamPull :exec
UPDATE commodore.streams
SET ingest_mode = 'pull', updated_at = NOW()
WHERE id = $1 AND tenant_id = $2;

-- name: InsertCreatedPullSource :exec
INSERT INTO commodore.stream_pull_sources (
    stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at
)
VALUES ($1, $2, $3, $4, NOW(), NOW());

-- name: SetCreatedStreamDescription :exec
UPDATE commodore.streams
SET description = $1
WHERE id = $2;

-- name: EnableCreatedStreamRecording :exec
UPDATE commodore.streams
SET is_recording_enabled = true
WHERE id = $1;

-- name: SetCreatedStreamLiveVideoABR :exec
UPDATE commodore.streams SET live_video_abr = $1
WHERE id = $2 AND tenant_id = $3;

-- name: GetStreamUpdateState :one
SELECT internal_name, ingest_mode, is_recording_enabled
FROM commodore.streams
WHERE id = $1 AND user_id = $2 AND tenant_id = $3;

-- name: GetStreamDVRChapterInterval :one
SELECT dvr_chapter_interval_seconds
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateStreamFields :execrows
UPDATE commodore.streams
SET title = CASE WHEN sqlc.arg(apply_title)::boolean THEN sqlc.arg(title) ELSE title END,
    description = CASE WHEN sqlc.arg(apply_description)::boolean THEN sqlc.arg(description) ELSE description END,
    is_recording_enabled = CASE WHEN sqlc.arg(apply_recording)::boolean THEN sqlc.arg(recording_enabled)::boolean ELSE is_recording_enabled END,
    dvr_chapter_mode = CASE WHEN sqlc.arg(apply_chapter_mode)::boolean THEN sqlc.narg(chapter_mode)::varchar ELSE dvr_chapter_mode END,
    dvr_chapter_interval_seconds = CASE WHEN sqlc.arg(apply_chapter_interval)::boolean THEN sqlc.narg(chapter_interval)::integer ELSE dvr_chapter_interval_seconds END,
    monitoring_enabled = CASE WHEN sqlc.arg(apply_monitoring)::boolean THEN sqlc.narg(monitoring_enabled)::boolean ELSE monitoring_enabled END,
    live_video_abr = CASE WHEN sqlc.arg(apply_live_video_abr)::boolean THEN sqlc.arg(live_video_abr)::varchar ELSE live_video_abr END,
    transcode_ladder = CASE WHEN sqlc.arg(apply_transcode_ladder)::boolean THEN sqlc.arg(transcode_ladder)::varchar ELSE transcode_ladder END,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND deleted_at IS NULL;

-- name: UpdatePullSourceFields :execrows
UPDATE commodore.stream_pull_sources
SET source_uri_enc = CASE WHEN sqlc.arg(apply_uri)::boolean THEN sqlc.arg(source_uri_enc) ELSE source_uri_enc END,
    enabled = CASE WHEN sqlc.arg(apply_enabled)::boolean THEN sqlc.arg(enabled)::boolean ELSE enabled END,
    allowed_cluster_ids = CASE WHEN sqlc.arg(apply_allowed)::boolean THEN sqlc.arg(allowed_cluster_ids)::text[] ELSE allowed_cluster_ids END,
    updated_at = NOW()
WHERE stream_id = sqlc.arg(stream_id)::uuid;

-- name: GetStreamForDeletion :one
SELECT internal_name, title
FROM commodore.streams
WHERE id = $1 AND user_id = $2 AND tenant_id = $3;

-- name: SoftDeleteStream :exec
UPDATE commodore.streams
SET deleted_at = COALESCE(deleted_at, NOW()), updated_at = NOW()
WHERE id = $1 AND tenant_id = $2;

-- name: FenceParentStreamLive :one
SELECT CASE WHEN deleted_at IS NULL THEN TRUE ELSE FALSE END::boolean AS live
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2
FOR UPDATE;

-- name: RefreshPrimaryStreamKey :execrows
UPDATE commodore.streams
SET stream_key = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3 AND tenant_id = $4
  AND ingest_mode = 'push' AND deleted_at IS NULL;

-- name: GetStreamPlaybackID :one
SELECT playback_id
FROM commodore.streams
WHERE id = $1 AND user_id = $2 AND tenant_id = $3;

-- name: GetStreamIngestModeForUser :one
SELECT ingest_mode
FROM commodore.streams
WHERE id = $1 AND user_id = $2 AND tenant_id = $3 AND deleted_at IS NULL;

-- name: StreamExistsForUser :one
SELECT EXISTS (
    SELECT 1 FROM commodore.streams
    WHERE id = $1 AND user_id = $2 AND tenant_id = $3 AND deleted_at IS NULL
) AS stream_exists;

-- name: GetCurrentPrimaryStreamKey :one
SELECT id, tenant_id, user_id, stream_key, created_at, updated_at
FROM commodore.streams
WHERE id = $1 AND user_id = $2 AND tenant_id = $3
  AND ingest_mode = 'push' AND deleted_at IS NULL;

-- name: SetCreatedStreamTranscodeLadder :exec
UPDATE commodore.streams SET transcode_ladder=sqlc.arg(transcode_ladder), updated_at=NOW()
WHERE id=sqlc.arg(stream_id)::uuid AND tenant_id=sqlc.arg(tenant_id)::uuid;
