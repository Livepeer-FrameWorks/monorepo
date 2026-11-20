-- name: GetDVRRecordingForTenant :one
SELECT d.id::text AS id,d.dvr_hash,d.internal_name,COALESCE(d.stream_id::text,'')::text AS stream_id,
       COALESCE(s.title,'Recording')::text AS title,d.playback_id,d.created_at,d.updated_at,
       d.retention_until,d.retention_source,d.size_bytes,d.duration,d.started_at,d.ended_at,
       d.lifecycle_status,d.storage_location,d.storage_cluster_id,d.origin_cluster_id,d.sync_status,d.is_synced,
       d.error_message,d.has_thumbnails,d.thumbnail_serving_cluster_id
FROM commodore.dvr_recordings d
LEFT JOIN commodore.streams s ON s.tenant_id=d.tenant_id AND s.id=d.stream_id
WHERE d.tenant_id=sqlc.arg(tenant_id)::uuid AND (d.id::text=sqlc.arg(recording_id)::text OR d.dvr_hash=sqlc.arg(recording_id)::text);

-- name: UpdateDVRRecordingCaptureTimes :execrows
UPDATE commodore.dvr_recordings
SET started_at=sqlc.narg(started_at)::timestamptz,ended_at=sqlc.narg(ended_at)::timestamptz
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND dvr_hash=sqlc.arg(artifact_hash)::text
  AND catalog_revision=sqlc.arg(source_revision)::bigint
  AND origin_cluster_id=sqlc.arg(source_cluster_id)::text;

-- name: GetDVRRecordingCaptureTimes :one
SELECT started_at,ended_at FROM commodore.dvr_recordings
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND dvr_hash=sqlc.arg(artifact_hash)::text;

-- name: ListChapterParentRecordings :many
SELECT chapter.artifact_hash,recording.id::text AS recording_id
FROM commodore.dvr_chapter_playback chapter
JOIN commodore.dvr_recordings recording ON recording.tenant_id=chapter.tenant_id AND recording.dvr_hash=chapter.dvr_hash
WHERE chapter.tenant_id=sqlc.arg(tenant_id)::uuid AND chapter.artifact_hash=ANY(sqlc.arg(artifact_hashes)::text[]);

-- name: StreamMediaCatalogExists :one
SELECT EXISTS(SELECT 1 FROM commodore.streams WHERE tenant_id=sqlc.arg(tenant_id)::uuid
  AND id=sqlc.arg(stream_id)::uuid AND deleted_at IS NULL)::boolean AS exists;
