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
