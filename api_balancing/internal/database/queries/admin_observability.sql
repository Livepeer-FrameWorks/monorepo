-- name: ListAdminArtifacts :many
SELECT a.artifact_hash, a.artifact_type, a.status, a.internal_name, a.tenant_id,
       a.storage_location, a.sync_status, a.s3_url, a.format, a.size_bytes,
       a.access_count, a.last_accessed_at, a.manifest_path, a.duration_seconds,
       a.dtsh_synced, COALESCE(a.retention_until::text, '')::text AS retention_until,
       a.created_at, a.updated_at,
       v.video_codec, v.audio_codec, v.resolution, v.duration_ms, v.bitrate_kbps,
       v.filename, v.title
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
WHERE a.status != 'deleted'
ORDER BY a.created_at DESC
LIMIT $1;

-- name: ListActiveArtifactNodes :many
SELECT node_id FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND NOT is_orphaned;

-- name: ListRecentProcessingJobs :many
SELECT job_id, tenant_id, artifact_hash, job_type, status, progress,
       use_gateway, processing_node_id, routing_reason, error_message, retry_count,
       created_at, started_at, completed_at
FROM foghorn.processing_jobs
WHERE status NOT IN ('completed', 'failed') OR created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT $1;
