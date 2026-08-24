-- name: GetRegistryArtifactByHash :one
SELECT artifact_hash, artifact_type,
       COALESCE(internal_name, '')::text AS internal_name,
       COALESCE(stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(stream_id::text, '')::text AS stream_id,
       COALESCE(tenant_id::text, '')::text AS tenant_id,
       COALESCE(status, '')::text AS status, COALESCE(format, '')::text AS format,
       COALESCE(origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(storage_cluster_id, '')::text AS storage_cluster_id,
       COALESCE(has_thumbnails, false)::boolean AS has_thumbnails
FROM foghorn.artifacts
WHERE artifact_hash = $1;

-- name: GetRegistryArtifactByInternalName :one
SELECT artifact_hash, artifact_type,
       COALESCE(internal_name, '')::text AS internal_name,
       COALESCE(stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(stream_id::text, '')::text AS stream_id,
       COALESCE(tenant_id::text, '')::text AS tenant_id,
       COALESCE(status, '')::text AS status, COALESCE(format, '')::text AS format,
       COALESCE(origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(storage_cluster_id, '')::text AS storage_cluster_id,
       COALESCE(has_thumbnails, false)::boolean AS has_thumbnails
FROM foghorn.artifacts
WHERE internal_name = $1;

-- name: GetRegistryProcessingJob :one
SELECT job_id::text AS job_id, COALESCE(tenant_id::text, '')::text AS tenant_id,
       COALESCE(status, '')::text AS status
FROM foghorn.processing_jobs
WHERE artifact_hash = $1
ORDER BY created_at DESC
LIMIT 1;
