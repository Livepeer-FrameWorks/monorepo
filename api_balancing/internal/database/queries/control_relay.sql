-- name: GetRelayArtifact :one
SELECT COALESCE(s3_url, '')::text AS s3_url, size_bytes, format, dtsh_synced,
       stream_internal_name, sync_status, origin_cluster_id, storage_cluster_id,
       tenant_id, artifact_type, COALESCE(active_dtsh_key, '')::text AS active_dtsh_key,
       COALESCE(durable_backend_local, false)::boolean AS durable_backend_local
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND status != 'deleted'
LIMIT 1;

-- name: GetRelayVodMetadata :one
SELECT vm.s3_key, a.size_bytes, a.tenant_id
FROM foghorn.vod_metadata vm
LEFT JOIN foghorn.artifacts a ON a.artifact_hash = vm.artifact_hash
WHERE vm.artifact_hash = sqlc.arg(artifact_hash)
LIMIT 1;

-- name: GetFreshRelayOriginNode :one
SELECT an.node_id, COALESCE(NULLIF(an.base_url, ''), no.base_url, '')::text AS base_url
FROM foghorn.artifact_nodes an
LEFT JOIN foghorn.node_outputs no ON no.node_id = an.node_id
WHERE an.artifact_hash = sqlc.arg(artifact_hash)
  AND an.role = 'origin'
  AND an.is_complete = true
  AND an.is_orphaned = false
  AND an.last_seen_at > NOW() - INTERVAL '90 seconds'
ORDER BY an.last_seen_at DESC
LIMIT 1;
