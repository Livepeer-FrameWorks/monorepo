-- These service-internal scheduler and telemetry queries span tenants. Cluster
-- entitlement is checked before placement; queue counts are operator metrics.
-- Infrastructure telemetry is scoped to registered node IDs from Quartermaster.
-- name: ListProcessingNodeBudgets :many
SELECT node_id, CASE WHEN lifecycle ? 'processing_memory_budget_bytes'
              AND COALESCE((lifecycle->>'cap_processing')::boolean,false)=false THEN '0'
            ELSE COALESCE(lifecycle->>'processing_memory_budget_bytes','') END::text AS budget_bytes
FROM foghorn.node_lifecycle
WHERE node_id = ANY(sqlc.arg(node_ids)::text[]);

-- Capacity changes cannot resolve a dispatch error or its retry backoff.
-- name: WakeProcessingClusterJobs :exec
UPDATE foghorn.processing_jobs AS job
SET next_attempt_at = NULL
WHERE job.status='queued'
  AND job.wait_reason IN ('memory', 'slots', 'fitting_node_unavailable', 'cluster_unavailable')
  AND EXISTS (SELECT 1 FROM foghorn.artifacts AS a
              WHERE a.artifact_hash=job.artifact_hash AND a.tenant_id=job.tenant_id
                AND a.origin_cluster_id=sqlc.arg(cluster_id)::text);

-- name: CountWaitingProcessingJobs :many
SELECT wait_reason, COUNT(*)::bigint AS jobs
FROM foghorn.processing_jobs
WHERE status='queued' AND wait_reason IS NOT NULL
GROUP BY wait_reason;

-- name: UpdateProcessingSourceProbe :execrows
UPDATE foghorn.processing_jobs
SET source_probe=sqlc.arg(source_probe)::jsonb,updated_at=NOW()
WHERE job_id=sqlc.arg(job_id) AND tenant_id::text=sqlc.arg(tenant_id)
  AND processing_node_id=sqlc.arg(node_id) AND status IN ('dispatched','processing');
