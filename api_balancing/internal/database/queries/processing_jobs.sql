-- name: ClaimQueuedProcessingJobs :many
WITH claimed AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'dispatched', updated_at = NOW()
    WHERE job_id IN (
        SELECT pj.job_id
        FROM foghorn.processing_jobs AS pj
        LEFT JOIN foghorn.artifacts AS a ON pj.artifact_hash = a.artifact_hash
        WHERE pj.status = 'queued'
          AND (a.status IS NULL OR a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted'))
        ORDER BY CASE WHEN a.artifact_type = 'clip' THEN 0 ELSE 1 END, pj.updated_at, pj.created_at
        LIMIT 20
        FOR UPDATE OF pj SKIP LOCKED
    )
    RETURNING job_id, tenant_id, artifact_hash, job_type, input_codec,
              output_profiles,
              retry_count, processes_json, source_url,
              source_params, preferred_node_id
)
SELECT c.job_id, c.tenant_id, c.artifact_hash,
       COALESCE(a.artifact_type, '')::text AS artifact_type,
       c.job_type, c.input_codec, c.output_profiles,
       'dispatched'::text AS status, c.retry_count,
       a.s3_url, c.source_url, c.source_params, c.preferred_node_id,
       c.processes_json, a.internal_name, COALESCE(a.stream_id::text, '')::text AS stream_id,
       a.stream_internal_name,
       COALESCE(a.durable_backend_local, false) AS durable_backend_local
FROM claimed AS c
LEFT JOIN foghorn.artifacts AS a ON c.artifact_hash = a.artifact_hash;

-- name: RevertProcessingJobToQueued :exec
UPDATE foghorn.processing_jobs
SET status = 'queued', processing_node_id = NULL, updated_at = NOW()
WHERE job_id = $1;

-- name: AssignProcessingJobNode :execrows
UPDATE foghorn.processing_jobs
SET processing_node_id = $2, updated_at = NOW()
WHERE job_id = $1 AND status = 'dispatched';

-- name: ProjectProcessingArtifactStatus :exec
UPDATE foghorn.artifacts
SET status = sqlc.arg(target_status)::text,
    error_message = CASE WHEN sqlc.arg(target_status)::text = 'processing' THEN NULL ELSE error_message END,
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND artifact_type IN ('clip', 'vod')
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- name: CommitDispatchedProcessingJob :execrows
UPDATE foghorn.processing_jobs
SET status = 'processing', processing_node_id = $2, routing_reason = $3,
    started_at = NOW(), updated_at = NOW()
WHERE job_id = $1 AND status = 'dispatched';

-- name: MarkProcessingArtifactStarted :execrows
UPDATE foghorn.artifacts
SET status = 'processing', error_message = NULL, updated_at = NOW()
WHERE artifact_hash = $1
  AND tenant_id::text = $2
  AND artifact_type IN ('clip', 'vod')
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- name: RequeueStaleProcessingJobs :execrows
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL,
        retry_count = retry_count + 1, updated_at = NOW()
    WHERE job.status IN ('dispatched', 'processing')
      AND job.updated_at < $1
      AND job.retry_count < $2
    RETURNING artifact_hash, tenant_id
)
UPDATE foghorn.artifacts AS a
SET status = 'queued', updated_at = NOW()
FROM requeued AS r
WHERE a.artifact_hash = r.artifact_hash
  AND a.tenant_id = r.tenant_id
  AND a.artifact_type IN ('clip', 'vod')
  AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- name: ListExhaustedProcessingJobIDs :many
SELECT job_id
FROM foghorn.processing_jobs
WHERE (status IN ('dispatched', 'processing') AND updated_at < $1 AND retry_count >= $2)
   OR (status = 'queued' AND created_at < $3
       AND preferred_node_id IS NOT NULL
       AND source_params->>'source_kind' IN ('live', 'dvr_rolling'));

-- name: FailExhaustedProcessingJob :one
WITH failed AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'failed',
        error_message = CASE WHEN job.status = 'queued'
            THEN 'stuck queued: node-pinned source unavailable'
            ELSE 'max retries exceeded' END,
        updated_at = NOW()
    WHERE job.job_id = $1
      AND ((job.status IN ('dispatched', 'processing') AND job.updated_at < $2 AND job.retry_count >= $3)
           OR (job.status = 'queued' AND job.created_at < $4
               AND job.preferred_node_id IS NOT NULL
               AND job.source_params->>'source_kind' IN ('live', 'dvr_rolling')))
    RETURNING artifact_hash, tenant_id, error_message
)
SELECT f.artifact_hash, COALESCE(a.artifact_type, '')::text AS artifact_type,
       COALESCE(a.tenant_id::text, f.tenant_id::text, '')::text AS tenant_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       f.error_message
FROM failed AS f
LEFT JOIN foghorn.artifacts AS a ON f.artifact_hash = a.artifact_hash;

-- name: MarkExhaustedArtifactFailed :execrows
UPDATE foghorn.artifacts
SET status = 'failed', error_message = $2, updated_at = NOW()
WHERE artifact_hash = $1
  AND tenant_id::text = $3
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- name: InsertProcessingJob :exec
INSERT INTO foghorn.processing_jobs
    (job_id, tenant_id, artifact_hash, job_type, status, parent_job_id,
     processes_json, source_url, source_params, preferred_node_id)
VALUES (sqlc.arg(job_id), sqlc.arg(tenant_id), sqlc.arg(artifact_hash), sqlc.arg(job_type),
        'queued', sqlc.narg(parent_job_id), sqlc.narg(processes_json),
        sqlc.narg(source_url), NULLIF(sqlc.narg(source_params)::text, '')::jsonb,
        sqlc.narg(preferred_node_id));

-- name: LockProcessingJobIdentity :exec
SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2));

-- name: FindActiveProcessingJob :one
SELECT job_id
FROM foghorn.processing_jobs
WHERE artifact_hash = $1
  AND job_type = $2
  AND status IN ('queued', 'dispatched', 'processing')
ORDER BY created_at
LIMIT 1;

-- name: MarkQueuedClipArtifact :exec
UPDATE foghorn.artifacts
SET status = 'queued', updated_at = NOW()
WHERE artifact_hash = $1
  AND tenant_id::text = $2
  AND artifact_type = 'clip'
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');
