-- name: GetArtifactOrigin :one
SELECT origin_type, origin_id
FROM foghorn.artifacts
WHERE artifact_hash = $1;

-- name: GetPlaybackArtifact :one
SELECT COALESCE(internal_name, '')::text AS internal_name,
       status, duration_seconds, size_bytes, created_at, format,
       COALESCE(storage_location, '')::text AS storage_location,
       COALESCE(sync_status, '')::text AS sync_status,
       COALESCE(has_thumbnails, false)::boolean AS has_thumbnails,
       COALESCE(storage_cluster_id, origin_cluster_id)::text AS authoritative_cluster,
       COALESCE(thumbnail_serving_cluster_id, storage_cluster_id, origin_cluster_id)::text AS thumbnail_serving_cluster
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = $2 AND status != 'deleted' AND tenant_id = $3;

-- name: GetDVRThumbnailTargetByInternalName :one
SELECT artifact_hash, tenant_id::text AS tenant_id,
       COALESCE(storage_cluster_id, origin_cluster_id)::text AS authoritative_cluster,
       COALESCE(has_thumbnails, false)::boolean AS has_thumbnails
FROM foghorn.artifacts
WHERE internal_name = $1 AND artifact_type = 'dvr';

-- name: GetDVRThumbnailTargetByChapterID :one
SELECT a.artifact_hash, a.tenant_id::text AS tenant_id,
       COALESCE(a.storage_cluster_id, a.origin_cluster_id)::text AS authoritative_cluster,
       COALESCE(a.has_thumbnails, false)::boolean AS has_thumbnails
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.artifact_hash
WHERE c.chapter_id = $1 AND a.artifact_type = 'dvr';
