-- name: GetProcessingJobAuthContext :one
SELECT pj.job_id::text AS job_id,
       pj.tenant_id::text AS tenant_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       pj.processes_json,
       pj.retry_count,
       COALESCE(pj.processing_node_id, '')::text AS processing_node_id,
       pj.status,
       vm.width,
       vm.height,
       vm.fps,
       COALESCE(NULLIF(pj.input_codec, ''), NULLIF(vm.video_codec, ''), '')::text AS input_codec
FROM foghorn.processing_jobs pj
LEFT JOIN foghorn.artifacts a ON a.artifact_hash = pj.artifact_hash
LEFT JOIN foghorn.vod_metadata vm ON vm.artifact_hash = pj.artifact_hash
WHERE pj.job_id::text = sqlc.arg(job_id)::text
  AND pj.artifact_hash = sqlc.arg(artifact_hash)::text
  AND pj.status IN ('dispatched', 'processing')
ORDER BY pj.updated_at DESC
LIMIT 1;

-- name: GetLiveTranscodeAuthContext :one
SELECT s.id::text AS session_id,
       s.tenant_id::text AS tenant_id,
       s.node_id,
       COALESCE(s.ingest_cluster_id, '')::text AS ingest_cluster_id,
       s.stream_internal_name,
       s.processes_json
FROM foghorn.ingest_sessions s
WHERE s.id::text = sqlc.arg(session_id)::text
  AND s.stream_internal_name = sqlc.arg(stream_internal_name)::text
  AND s.ended_at IS NULL
  AND s.projection_state = 'active'
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
