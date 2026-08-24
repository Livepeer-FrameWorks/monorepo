-- name: GetProcessingJobAuthContext :one
SELECT pj.tenant_id::text AS tenant_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       pj.processes_json,
       vm.width,
       vm.height,
       vm.fps
FROM foghorn.processing_jobs pj
LEFT JOIN foghorn.artifacts a ON a.artifact_hash = pj.artifact_hash
LEFT JOIN foghorn.vod_metadata vm ON vm.artifact_hash = pj.artifact_hash
WHERE pj.artifact_hash = sqlc.arg(artifact_hash)::text
  AND pj.status IN ('queued', 'dispatched', 'processing')
ORDER BY pj.updated_at DESC
LIMIT 1;

-- name: GetDVRArtifactTenantID :one
SELECT tenant_id::text
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: GetArtifactRelayDescriptor :one
SELECT format,
       artifact_type,
       stream_internal_name,
       COALESCE(storage_cluster_id, origin_cluster_id, '')::text AS authoritative_cluster
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND status != 'deleted'
LIMIT 1;
