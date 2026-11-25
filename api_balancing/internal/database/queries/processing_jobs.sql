-- name: ClaimQueuedProcessingJobs :many
WITH claimed AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'dispatched', updated_at = NOW(), created_at=COALESCE(job.created_at,NOW() AT TIME ZONE 'UTC')
    WHERE job_id IN (
        SELECT pj.job_id
        FROM foghorn.processing_jobs AS pj
        LEFT JOIN foghorn.artifacts AS a ON pj.artifact_hash = a.artifact_hash AND pj.tenant_id = a.tenant_id
        WHERE pj.status = 'queued'
          AND (pj.next_attempt_at IS NULL OR pj.next_attempt_at <= NOW())
          AND (a.status IS NULL OR a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
               OR (pj.job_type = 'thumbnail' AND a.status = 'ready'))
        ORDER BY pj.created_at, pj.job_id
        LIMIT 20
        FOR UPDATE OF pj SKIP LOCKED
    )
    RETURNING job_id, tenant_id, artifact_hash, job_type, input_codec,
              output_profiles,
              retry_count, processes_json, source_url,
              source_params, preferred_node_id, required_memory_bytes, memory_floor_bytes, wait_reason, wait_started_at, source_probe, created_at
)
SELECT c.job_id, c.tenant_id, c.artifact_hash,
       COALESCE(a.artifact_type, '')::text AS artifact_type,
       c.job_type, c.input_codec, c.output_profiles,
       'dispatched'::text AS status, c.retry_count,
       a.s3_url, c.source_url, c.source_params, c.preferred_node_id,
       c.processes_json, a.internal_name, COALESCE(a.stream_id::text, '')::text AS stream_id,
       a.stream_internal_name,
       COALESCE(a.durable_backend_local, false) AS durable_backend_local,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id, c.required_memory_bytes, c.memory_floor_bytes, c.wait_reason, c.wait_started_at, c.source_probe, c.created_at, COALESCE(a.created_at,c.created_at) AS upload_created_at
FROM claimed AS c
LEFT JOIN foghorn.artifacts AS a ON c.artifact_hash = a.artifact_hash AND c.tenant_id = a.tenant_id
ORDER BY c.created_at,c.job_id;

-- name: RevertProcessingJobToQueued :exec
UPDATE foghorn.processing_jobs
SET status = 'queued', processing_node_id = NULL, updated_at = NOW(),
    next_attempt_at=NOW()+INTERVAL '15 seconds',
    wait_reason='cluster_unavailable',
    memory_wait_ms = memory_wait_ms + CASE WHEN wait_reason='memory' THEN GREATEST(0, EXTRACT(EPOCH FROM ((NOW() AT TIME ZONE 'UTC')-COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC')))*1000)::bigint ELSE 0 END,
        memory_wait_started_at = NULL,
        wait_started_at = CASE WHEN wait_reason='cluster_unavailable' THEN COALESCE(wait_started_at,NOW() AT TIME ZONE 'UTC') ELSE NOW() AT TIME ZONE 'UTC' END
WHERE job_id = $1;

-- Returns a claimed job no node can run to the queue and keeps it out of
-- ClaimQueuedProcessingJobs until next_attempt_at.
-- Fairness counts time actually blocked by memory, separately from routing waits.
-- name: DeferUnroutableProcessingJob :exec
WITH deferred AS (
 UPDATE foghorn.processing_jobs AS job
 SET status = 'queued', processing_node_id = NULL, updated_at = NOW(),
     next_attempt_at = NOW() + make_interval(secs => sqlc.arg(defer_seconds)::double precision),
     wait_reason = sqlc.arg(wait_reason)::text,
     memory_wait_ms = memory_wait_ms + CASE WHEN (sqlc.arg(wait_reason)::text) IS DISTINCT FROM 'memory' THEN CASE WHEN wait_reason='memory' THEN GREATEST(0, EXTRACT(EPOCH FROM ((NOW() AT TIME ZONE 'UTC')-COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC')))*1000)::bigint ELSE 0 END ELSE 0 END,
        memory_wait_started_at = CASE WHEN (sqlc.arg(wait_reason)::text)='memory' THEN CASE WHEN wait_reason='memory' THEN COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC') ELSE NOW() AT TIME ZONE 'UTC' END ELSE NULL END,
        wait_started_at = CASE WHEN (sqlc.arg(wait_reason)::text) IS NULL THEN NULL WHEN wait_reason=(sqlc.arg(wait_reason)::text) THEN COALESCE(wait_started_at,NOW() AT TIME ZONE 'UTC') ELSE NOW() AT TIME ZONE 'UTC' END
 WHERE job.job_id = sqlc.arg(job_id) AND job.tenant_id::text = sqlc.arg(tenant_id)
 RETURNING job.artifact_hash,job.tenant_id
)
UPDATE foghorn.artifacts AS a
SET catalog_revision=GREATEST(a.catalog_revision+1,4503599627370496),updated_at=NOW()
FROM deferred d
WHERE a.artifact_hash=d.artifact_hash AND a.tenant_id=d.tenant_id;

-- name: AssignProcessingJobNode :execrows
UPDATE foghorn.processing_jobs
SET processing_node_id = $2, updated_at = NOW(),
    progress_last_ms = 0, progress_advanced_at = NOW()
WHERE job_id = $1 AND status = 'dispatched' AND tenant_id::text=NULLIF(sqlc.arg(tenant_id)::text,'');

-- name: ProjectProcessingArtifactStatus :exec
UPDATE foghorn.artifacts
SET status = sqlc.arg(target_status)::text,
    error_message = CASE WHEN sqlc.arg(target_status)::text = 'processing' THEN NULL ELSE error_message END,
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id::text = sqlc.arg(tenant_id)
  AND artifact_type IN ('clip', 'vod')
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- Clip, thumbnail and chapter handlers have no later memory-admission report;
-- dispatch ends their wait and releases their fairness protection.
-- name: CommitDispatchedProcessingJob :execrows
UPDATE foghorn.processing_jobs
SET status = 'processing', wait_reason = NULL,
        memory_wait_ms = CASE WHEN job_type IN ('thumbnail','dvr_chapter_finalize')
            OR (BTRIM(source_params->>'source_kind') IN ('live','dvr_rolling','chapter')
                AND NULLIF(BTRIM(source_params->>'source_stream_name'),'') IS NOT NULL) THEN 0
            ELSE memory_wait_ms + CASE WHEN wait_reason='memory' THEN GREATEST(0, EXTRACT(EPOCH FROM ((NOW() AT TIME ZONE 'UTC')-COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC')))*1000)::bigint ELSE 0 END END,
        memory_admitted_at = CASE WHEN job_type IN ('thumbnail','dvr_chapter_finalize')
            OR (BTRIM(source_params->>'source_kind') IN ('live','dvr_rolling','chapter')
                AND NULLIF(BTRIM(source_params->>'source_stream_name'),'') IS NOT NULL)
            THEN NOW() AT TIME ZONE 'UTC' ELSE memory_admitted_at END,
        memory_wait_started_at = NULL,
        wait_started_at = NULL, processing_node_id = $2, routing_reason = $3,
    started_at = NOW(), updated_at = NOW(),
    progress_advanced_at = COALESCE(progress_advanced_at, NOW())
WHERE job_id = $1 AND status = 'dispatched' AND tenant_id::text=NULLIF(sqlc.arg(tenant_id)::text,'');

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
        retry_count = retry_count + 1, updated_at = NOW(),
        progress_last_ms = 0, progress_advanced_at = NULL
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
FROM foghorn.processing_jobs AS job
WHERE (status IN ('dispatched', 'processing') AND updated_at < sqlc.narg(updated_at)::timestamp AND retry_count >= sqlc.narg(retry_count)::integer)
   OR (status IN ('dispatched', 'processing') AND progress_advanced_at < sqlc.narg(progress_advanced_at)::timestamp)
   OR (status='queued' AND EXISTS (SELECT 1 FROM foghorn.artifacts gone WHERE gone.artifact_hash=job.artifact_hash AND gone.tenant_id=job.tenant_id AND gone.status IN ('failed','deleted','expired','aborted')))
   OR (status IN ('queued', 'dispatched', 'processing') AND COALESCE(
       (SELECT CASE WHEN upload.artifact_type='vod' AND job.job_type='transcode' THEN upload.created_at ELSE job.created_at END
        FROM foghorn.artifacts AS upload WHERE upload.artifact_hash=job.artifact_hash AND upload.tenant_id=job.tenant_id),
       job.created_at) < sqlc.arg(upload_deadline)::timestamp);

-- name: FailExhaustedProcessingJob :one
WITH failed AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'failed', wait_reason = NULL, wait_started_at = NULL,
        error_message = CASE
            WHEN COALESCE((SELECT CASE WHEN upload.artifact_type='vod' AND job.job_type='transcode' THEN upload.created_at ELSE job.created_at END
                  FROM foghorn.artifacts AS upload WHERE upload.artifact_hash=job.artifact_hash AND upload.tenant_id=job.tenant_id),job.created_at)<sqlc.arg(upload_deadline)::timestamp
              THEN CASE WHEN EXISTS (SELECT 1 FROM foghorn.artifacts AS upload WHERE upload.artifact_hash=job.artifact_hash AND upload.tenant_id=job.tenant_id AND upload.artifact_type='vod' AND job.job_type='transcode') THEN 'upload processing timed out after 24 hours' ELSE 'processing timed out after 24 hours' END
            WHEN EXISTS (SELECT 1 FROM foghorn.artifacts gone WHERE gone.artifact_hash=job.artifact_hash AND gone.tenant_id=job.tenant_id AND gone.status IN ('failed','deleted','expired','aborted')) THEN 'source artifact is no longer available'
            WHEN job.updated_at < sqlc.narg(updated_at)::timestamp AND job.retry_count >= sqlc.narg(retry_count)::integer THEN 'max retries exceeded'
            ELSE 'no processing progress within the watchdog window' END,
        updated_at = NOW()
    WHERE job.job_id = sqlc.arg(job_id)
      AND ((job.status IN ('dispatched', 'processing') AND job.updated_at < sqlc.narg(updated_at)::timestamp AND job.retry_count >= sqlc.narg(retry_count)::integer)
           OR (job.status IN ('dispatched', 'processing') AND job.progress_advanced_at < sqlc.narg(progress_advanced_at)::timestamp)
           OR (job.status='queued' AND EXISTS (SELECT 1 FROM foghorn.artifacts gone WHERE gone.artifact_hash=job.artifact_hash AND gone.tenant_id=job.tenant_id AND gone.status IN ('failed','deleted','expired','aborted')))
           OR (job.status IN ('queued', 'dispatched', 'processing') AND COALESCE(
             (SELECT CASE WHEN upload.artifact_type='vod' AND job.job_type='transcode' THEN upload.created_at ELSE job.created_at END
              FROM foghorn.artifacts AS upload WHERE upload.artifact_hash=job.artifact_hash AND upload.tenant_id=job.tenant_id),job.created_at)<sqlc.arg(upload_deadline)::timestamp))
    RETURNING artifact_hash, tenant_id, error_message
)
SELECT f.artifact_hash, COALESCE(a.artifact_type, '')::text AS artifact_type,
       COALESCE(a.tenant_id::text, f.tenant_id::text, '')::text AS tenant_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       f.error_message
FROM failed AS f
LEFT JOIN foghorn.artifacts AS a ON f.artifact_hash = a.artifact_hash AND f.tenant_id=a.tenant_id;

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

-- Requeues the jobs assigned to a node before its registration that the node
-- did not report as running: a restarted sidecar lost them.
-- name: RequeueUnreportedNodeProcessingJobs :execrows
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL,
        retry_count = retry_count + 1, updated_at = NOW(),
        progress_last_ms = 0, progress_advanced_at = NULL
    WHERE job.processing_node_id = sqlc.arg(node_id)
      AND job.status IN ('dispatched', 'processing')
      -- Assignment time, not updated_at: a running job's progress moves
      -- updated_at every second, which would exclude every active job.
      AND COALESCE(job.started_at, job.updated_at) < sqlc.arg(assigned_before)
      AND job.retry_count < sqlc.arg(max_retries)
      AND NOT (job.job_id::text = ANY(sqlc.arg(reported_job_ids)::text[]))
    RETURNING artifact_hash, tenant_id
)
UPDATE foghorn.artifacts AS a
SET status = 'queued', updated_at = NOW()
FROM requeued AS r
WHERE a.artifact_hash = r.artifact_hash
  AND a.tenant_id = r.tenant_id
  AND a.artifact_type IN ('clip', 'vod')
  AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- name: RequeueSilentNodeProcessingJobs :execrows
-- A job assigned before the node registered that has not written since:
-- assignment and every lease renewal move updated_at, so it never reached
-- the node's current connection.
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL,
        retry_count = retry_count + 1, updated_at = NOW(),
        progress_last_ms = 0, progress_advanced_at = NULL
    WHERE job.processing_node_id = sqlc.arg(node_id)
      AND job.status IN ('dispatched', 'processing')
      AND job.updated_at < sqlc.arg(registered_at)
      AND job.retry_count < sqlc.arg(max_retries)
    RETURNING artifact_hash, tenant_id
)
UPDATE foghorn.artifacts AS a
SET status = 'queued', updated_at = NOW()
FROM requeued AS r
WHERE a.artifact_hash = r.artifact_hash
  AND a.tenant_id = r.tenant_id
  AND a.artifact_type IN ('clip', 'vod')
  AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');

-- Requeues a job its node reported as failed in a retryable way, while the
-- retry budget lasts. Bound to the reporting node like every result. The job
-- keeps processes_json, so the next attempt runs the persisted ladder.
-- name: RequeueRetryableProcessingJob :one
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL,
        retry_count = retry_count + 1, updated_at = NOW(),
        error_message = sqlc.arg(error_message),
        progress_last_ms = 0, progress_advanced_at = NULL
    WHERE job.job_id = sqlc.arg(job_id)
      AND job.processing_node_id = sqlc.arg(node_id)
      AND job.status IN ('dispatched', 'processing')
      AND job.retry_count < sqlc.arg(max_retries)
    RETURNING artifact_hash, tenant_id
), artifact AS (
    UPDATE foghorn.artifacts AS a
    SET status = 'queued', updated_at = NOW()
    FROM requeued AS r
    WHERE a.artifact_hash = r.artifact_hash
      AND a.tenant_id = r.tenant_id
      AND a.artifact_type IN ('clip', 'vod')
      AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
    RETURNING a.artifact_hash
)
SELECT COUNT(*)::int AS requeued FROM requeued;

-- Requeues a job whose node reported its source read stalled in storage or
-- upstream. The attempt does not draw from the retry budget: it is bounded by
-- how long the run of stalls has lasted, measured from the first stalled
-- attempt's start, and backs off by that same elapsed time, never past the end
-- of the window. Bound to the reporting node like every result.
-- name: RequeueStalledProcessingJob :one
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL, updated_at = NOW(),
        error_message = sqlc.arg(error_message),
        progress_last_ms = 0, progress_advanced_at = NULL,
        source_stalled_since = COALESCE(job.source_stalled_since, job.started_at, NOW()),
        next_attempt_at = LEAST(
            NOW() + (NOW() - COALESCE(job.source_stalled_since, job.started_at, NOW())),
            COALESCE(job.source_stalled_since, job.started_at, NOW()) + CAST(sqlc.arg(stall_window) AS text)::interval)
    WHERE job.job_id = sqlc.arg(job_id)
      AND job.processing_node_id = sqlc.arg(node_id)
      AND job.status IN ('dispatched', 'processing')
      AND COALESCE(job.source_stalled_since, job.started_at, NOW()) > NOW() - CAST(sqlc.arg(stall_window) AS text)::interval
    RETURNING artifact_hash, tenant_id
), artifact AS (
    UPDATE foghorn.artifacts AS a
    SET status = 'queued', updated_at = NOW()
    FROM requeued AS r
    WHERE a.artifact_hash = r.artifact_hash
      AND a.tenant_id = r.tenant_id
      AND a.artifact_type IN ('clip', 'vod')
      AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
    RETURNING a.artifact_hash
)
SELECT COUNT(*)::int AS requeued FROM requeued;

-- Capacity backpressure is not a failed processing attempt. Only the assigned
-- node can return an active job, and the queue delay bounds redispatch traffic.
-- name: RequeueCapacityDeferredProcessingJob :one
WITH requeued AS (
    UPDATE foghorn.processing_jobs AS job
    SET status = 'queued', processing_node_id = NULL, updated_at = NOW(),
        error_message = sqlc.arg(error_message),
        progress_last_ms = 0, progress_advanced_at = NULL,
        next_attempt_at = NOW() + INTERVAL '30 seconds',
        memory_floor_bytes = GREATEST(memory_floor_bytes,sqlc.arg(memory_floor_bytes)::bigint),
        required_memory_bytes = GREATEST(memory_floor_bytes,sqlc.arg(memory_floor_bytes)::bigint,sqlc.arg(required_memory_bytes)::bigint),
        processes_json = COALESCE(NULLIF(sqlc.arg(processes_json)::text, ''), processes_json),
        wait_reason = CASE WHEN sqlc.arg(node_too_small)::boolean OR sqlc.arg(node_unavailable)::boolean THEN 'fitting_node_unavailable' ELSE 'memory' END,
        memory_wait_ms = memory_wait_ms + CASE WHEN (CASE WHEN sqlc.arg(node_too_small)::boolean OR sqlc.arg(node_unavailable)::boolean THEN 'fitting_node_unavailable' ELSE 'memory' END) IS DISTINCT FROM 'memory' THEN CASE WHEN wait_reason='memory' THEN GREATEST(0, EXTRACT(EPOCH FROM ((NOW() AT TIME ZONE 'UTC')-COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC')))*1000)::bigint ELSE 0 END ELSE 0 END,
        memory_wait_started_at = CASE WHEN (CASE WHEN sqlc.arg(node_too_small)::boolean OR sqlc.arg(node_unavailable)::boolean THEN 'fitting_node_unavailable' ELSE 'memory' END)='memory' THEN CASE WHEN wait_reason='memory' THEN COALESCE(memory_wait_started_at,wait_started_at,NOW() AT TIME ZONE 'UTC') ELSE NOW() AT TIME ZONE 'UTC' END ELSE NULL END,
        wait_started_at = CASE WHEN (CASE WHEN sqlc.arg(node_too_small)::boolean OR sqlc.arg(node_unavailable)::boolean THEN 'fitting_node_unavailable' ELSE 'memory' END) IS NULL THEN NULL WHEN wait_reason=(CASE WHEN sqlc.arg(node_too_small)::boolean OR sqlc.arg(node_unavailable)::boolean THEN 'fitting_node_unavailable' ELSE 'memory' END) THEN COALESCE(wait_started_at,NOW() AT TIME ZONE 'UTC') ELSE NOW() AT TIME ZONE 'UTC' END
    WHERE job.job_id = sqlc.arg(job_id)
      AND job.processing_node_id = sqlc.arg(node_id)::text
      AND job.tenant_id::text = sqlc.arg(tenant_id)
      AND job.status IN ('dispatched', 'processing')
      AND EXISTS (SELECT 1 FROM foghorn.artifacts AS owned WHERE owned.artifact_hash=job.artifact_hash AND owned.tenant_id=job.tenant_id AND owned.status NOT IN ('deleted','expired','aborted','failed'))
    RETURNING artifact_hash, tenant_id
), artifact AS (
    UPDATE foghorn.artifacts AS a
    SET status = 'queued', catalog_revision=GREATEST(a.catalog_revision+1,4503599627370496), updated_at = NOW()
    FROM requeued AS r
    WHERE a.artifact_hash = r.artifact_hash
      AND a.tenant_id = r.tenant_id
      AND a.artifact_type IN ('clip', 'vod')
      AND a.status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
    RETURNING a.artifact_hash
)
SELECT COUNT(*)::int AS requeued FROM requeued;

-- Dispatch persists only the policy. Memory admission is acknowledged by the
-- edge after its durable ledger acquires the reservation.
-- name: CacheProcessingJobDispatchPolicy :execrows
UPDATE foghorn.processing_jobs
SET processes_json=sqlc.arg(processes_json),updated_at=NOW()
WHERE job_id=sqlc.arg(job_id) AND tenant_id::text=sqlc.arg(tenant_id)
 AND processing_node_id=sqlc.arg(node_id) AND status='dispatched';

-- The dispatcher is a cluster-wide scheduler. These identities are subsequently
-- checked against node liveness and terminalized in tenant-bound transactions.
-- name: ListQueuedPinnedProcessingJobs :many
SELECT job_id,tenant_id,preferred_node_id FROM foghorn.processing_jobs
WHERE status='queued' AND preferred_node_id IS NOT NULL AND preferred_node_id<>''
  AND source_params->>'source_kind' IN ('live','dvr_rolling');
