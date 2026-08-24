-- name: ListStaleDVRStartObligations :many
SELECT a.artifact_hash, a.tenant_id::text AS tenant_id, a.status,
       a.dvr_start_dispatch::text AS dvr_start_dispatch
FROM foghorn.artifacts a
WHERE a.artifact_type = 'dvr'
  AND a.dvr_start_dispatch IS NOT NULL
  AND a.updated_at < NOW() - sqlc.arg(stale_seconds)::bigint * INTERVAL '1 second'
  AND ((a.status IN ('requested', 'starting') AND a.dvr_start_dispatch->>'state' = 'pending')
       OR a.dvr_start_dispatch->>'state' = 'stop_pending')
ORDER BY a.updated_at
LIMIT sqlc.arg(batch_limit);

-- name: IngestGenerationEnded :one
SELECT (ended_at IS NOT NULL)::boolean AS ended
FROM foghorn.ingest_sessions
WHERE id = sqlc.arg(ingest_generation)::uuid AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: MarkDVRStopPending :exec
UPDATE foghorn.artifacts
SET dvr_start_dispatch = jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{state}', '"stop_pending"'::jsonb),
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status IN ('requested', 'starting', 'recording');
