-- name: GetTenantMediaFootprint :one
-- Use the same origin-owned bytes as the write cap, including retained upload
-- originals without charging the same source object twice. One statement keeps
-- completion from hiding an unknown-size artifact between separate reads.
SELECT COALESCE(SUM(
       CASE WHEN v.original_s3_key IS NOT NULL AND a.active_object_key = v.original_s3_key
                      AND (a.sync_status = 'synced' OR a.status IN ('queued', 'processing'))
            THEN GREATEST(COALESCE(a.size_bytes, 0), COALESCE(v.original_size_bytes, 0))
            ELSE COALESCE(a.size_bytes, 0) + COALESCE(v.original_size_bytes, 0) END),0)::bigint AS storage_bytes,
       COUNT(*) FILTER (WHERE COALESCE(a.status,'unknown') NOT IN ('ready','completed','completed_partial')
                          OR a.size_bytes IS NULL OR a.size_bytes < 0
                          OR (v.original_s3_key IS NOT NULL AND v.original_size_bytes IS NULL))::bigint AS pending_artifacts,
       COUNT(*) FILTER (WHERE sqlc.arg(retention_limit_days)::integer > 0
                          AND (a.retention_until IS NULL OR a.retention_until > NOW() + sqlc.arg(retention_limit_days)::integer * INTERVAL '1 day'))::bigint AS incompatible_retention,
       (SELECT COUNT(*) FROM (
          SELECT stream_internal_name FROM foghorn.ingest_sessions
          WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND ended_at IS NULL
          UNION
          SELECT stream_internal_name FROM foghorn.pull_ingest_sessions
          WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND ended_at IS NULL
       ) active_streams)::bigint AS active_ingest
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.tenant_id = sqlc.arg(tenant_id)::uuid AND a.federated_pointer = false
  AND COALESCE(a.status,'unknown') NOT IN ('failed','expired','deleted','aborted');
