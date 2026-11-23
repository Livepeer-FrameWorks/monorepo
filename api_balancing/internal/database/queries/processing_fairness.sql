-- Fairness arbitrates shared node capacity across tenants. This scheduler-only
-- inventory is global; reservation mutations bind the owning job and tenant.
-- Cleared rows retain their revision so a delayed protection cannot undo a clear.
-- name: ClearFinishedProcessingReservations :exec
UPDATE foghorn.processing_node_reservations AS reservation
SET job_id=NULL,tenant_id=NULL,required_memory_bytes=0,
    revision=nextval('foghorn.processing_reservation_revision')
WHERE reservation.required_memory_bytes>0 AND NOT EXISTS (
 SELECT 1 FROM foghorn.processing_jobs AS job
 WHERE job.job_id=reservation.job_id AND job.tenant_id=reservation.tenant_id
   AND job.status IN ('queued','dispatched','processing') AND job.memory_admitted_at IS NULL
);

-- name: ListProcessingMemoryReservations :many
SELECT node_id,job_id,COALESCE(tenant_id::text,'')::text AS tenant_id,required_memory_bytes,revision
FROM foghorn.processing_node_reservations;

-- name: GetProcessingMemoryReservation :one
SELECT node_id,job_id,COALESCE(tenant_id::text,'')::text AS tenant_id,required_memory_bytes,revision
FROM foghorn.processing_node_reservations WHERE node_id=$1;

-- name: ReserveProcessingNode :execrows
INSERT INTO foghorn.processing_node_reservations(node_id,job_id,tenant_id,required_memory_bytes)
SELECT sqlc.arg(node_id)::text,job.job_id,job.tenant_id,sqlc.arg(required_bytes)::bigint
FROM foghorn.processing_jobs AS job
JOIN foghorn.artifacts AS asset ON asset.artifact_hash=job.artifact_hash AND asset.tenant_id=job.tenant_id
WHERE job.job_id=sqlc.arg(job_id) AND job.tenant_id::text=sqlc.arg(tenant_id)
  AND job.status IN ('queued','dispatched','processing') AND (job.wait_reason='memory' OR job.memory_wait_ms>0)
  AND job.memory_admitted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.processing_jobs AS older
    JOIN foghorn.artifacts AS other ON other.artifact_hash=older.artifact_hash AND other.tenant_id=older.tenant_id
    WHERE other.origin_cluster_id=asset.origin_cluster_id
      AND (other.status NOT IN ('ready','failed','deleted','expired','aborted') OR (older.job_type='thumbnail' AND other.status='ready')) AND (older.wait_reason='memory' OR older.memory_wait_ms>0)
      AND older.status IN ('queued','dispatched','processing') AND older.memory_admitted_at IS NULL
      AND (make_interval(secs => older.memory_wait_ms/1000.0) + CASE WHEN older.wait_reason='memory' THEN GREATEST(INTERVAL '0 seconds',(NOW() AT TIME ZONE 'UTC')-COALESCE(older.memory_wait_started_at,older.wait_started_at,NOW() AT TIME ZONE 'UTC')) ELSE INTERVAL '0 seconds' END) > ((NOW() AT TIME ZONE 'UTC')-sqlc.arg(wait_threshold)::timestamp)
      AND COALESCE(CASE WHEN other.artifact_type='vod' AND older.job_type='transcode' THEN other.created_at ELSE older.created_at END,older.created_at)>(NOW() AT TIME ZONE 'UTC')-INTERVAL '24 hours'
      AND older.required_memory_bytes<=sqlc.arg(node_budget)::bigint
      AND NOT EXISTS (SELECT 1 FROM foghorn.processing_node_reservations AS held WHERE held.job_id=older.job_id AND held.node_id<>sqlc.arg(node_id)::text AND held.required_memory_bytes>0)
      AND (older.preferred_node_id IS NULL OR older.preferred_node_id=sqlc.arg(node_id)::text)
      AND (older.created_at,older.job_id)<(job.created_at,job.job_id)
  )
ON CONFLICT(node_id) DO UPDATE SET
 job_id=EXCLUDED.job_id,tenant_id=EXCLUDED.tenant_id,required_memory_bytes=EXCLUDED.required_memory_bytes,
 revision=nextval('foghorn.processing_reservation_revision')
WHERE foghorn.processing_node_reservations.job_id IS NULL
   OR (foghorn.processing_node_reservations.job_id=EXCLUDED.job_id
       AND foghorn.processing_node_reservations.tenant_id=EXCLUDED.tenant_id
       AND foghorn.processing_node_reservations.required_memory_bytes<>EXCLUDED.required_memory_bytes);

-- name: CancelProcessingNodeReservation :execrows
UPDATE foghorn.processing_node_reservations
SET job_id=NULL,tenant_id=NULL,required_memory_bytes=0,
    revision=nextval('foghorn.processing_reservation_revision')
WHERE job_id=sqlc.arg(job_id) AND tenant_id::text=sqlc.arg(tenant_id);

-- name: ListProcessingMemoryWaiters :many
SELECT job.job_id,job.tenant_id::text,job.required_memory_bytes,job.preferred_node_id,
       asset.origin_cluster_id,job.created_at,job.wait_started_at
FROM foghorn.processing_jobs AS job
JOIN foghorn.artifacts AS asset ON asset.artifact_hash=job.artifact_hash AND asset.tenant_id=job.tenant_id
WHERE job.status IN ('queued','dispatched','processing') AND (job.wait_reason='memory' OR job.memory_wait_ms>0)
 AND job.memory_admitted_at IS NULL AND job.required_memory_bytes>0
 AND (make_interval(secs => job.memory_wait_ms/1000.0) + CASE WHEN job.wait_reason='memory' THEN GREATEST(INTERVAL '0 seconds',(NOW() AT TIME ZONE 'UTC')-COALESCE(job.memory_wait_started_at,job.wait_started_at,NOW() AT TIME ZONE 'UTC')) ELSE INTERVAL '0 seconds' END) > ((NOW() AT TIME ZONE 'UTC')-sqlc.arg(wait_threshold)::timestamp)
 AND (asset.status NOT IN ('ready','failed','deleted','expired','aborted')
      OR (job.job_type='thumbnail' AND asset.status='ready'))
 AND COALESCE(CASE WHEN asset.artifact_type='vod' AND job.job_type='transcode' THEN asset.created_at ELSE job.created_at END,job.created_at)>sqlc.arg(upload_deadline)::timestamp
ORDER BY job.created_at,job.job_id;
