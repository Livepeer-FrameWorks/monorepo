-- name: LatestActiveProcessingSourceURL :one
SELECT source_url
FROM foghorn.processing_jobs
WHERE artifact_hash = $1
  AND status IN ('dispatched', 'processing')
  AND source_url IS NOT NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: UploadedArtifactFormat :one
SELECT COALESCE(format, '')::text AS format
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND s3_url IS NOT NULL;

-- name: LatestActiveProcessingConfig :one
SELECT processes_json
FROM foghorn.processing_jobs
WHERE artifact_hash = $1
  AND status IN ('queued', 'dispatched', 'processing')
ORDER BY created_at DESC
LIMIT 1;

-- name: RollingDVRProcessConfig :one
SELECT dvr_processes_json
FROM foghorn.artifacts
WHERE internal_name = $1 AND artifact_type = 'dvr';

-- name: ActiveLiveProcessConfig :one
SELECT processes_json
FROM foghorn.ingest_sessions
WHERE stream_internal_name = sqlc.arg(stream_internal_name)
  AND ended_at IS NULL
  AND projection_state = 'active'
ORDER BY started_at DESC
LIMIT 1;

-- name: ActiveLiveTranscodeJobContext :one
SELECT id::text AS session_id,
       tenant_id::text AS tenant_id,
       node_id,
       COALESCE(ingest_cluster_id, '')::text AS cluster_id,
       stream_internal_name,
       processes_json
FROM foghorn.ingest_sessions
WHERE stream_internal_name = sqlc.arg(stream_internal_name)
  AND ended_at IS NULL
  AND projection_state = 'active'
ORDER BY started_at DESC
LIMIT 1;

-- name: ActiveProcessingTranscodeJobContext :one
SELECT job_id::text AS job_id,
       tenant_id::text AS tenant_id,
       artifact_hash,
       retry_count,
       COALESCE(processing_node_id, '')::text AS processing_node_id,
       processes_json
FROM foghorn.processing_jobs
WHERE artifact_hash = sqlc.arg(artifact_hash)::text
  AND status IN ('dispatched', 'processing')
  AND processing_node_id IS NOT NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: ActiveChapterTranscodeJobContext :one
SELECT c.chapter_id,
       c.finalize_attempts,
       COALESCE(c.finalize_node_id, '')::text AS processing_node_id,
       COALESCE(c.finalize_processes_json, '')::text AS processes_json,
       a.tenant_id::text AS tenant_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
WHERE c.playback_artifact_hash = sqlc.arg(artifact_hash)::text
  AND c.state = 'finalizing'
  AND c.finalize_node_id IS NOT NULL
  AND c.finalize_processes_json IS NOT NULL
LIMIT 1;
