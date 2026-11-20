-- name: ListClaimedIngestStopTargets :many
SELECT id::text AS session_id,node_id,connector_pid,COALESCE(NULLIF(runtime_name,''),'live+'||stream_internal_name)::text AS runtime_name,ended_at,COALESCE(ended_reason,'')::text AS ended_reason,ingest_disconnect_requested_at,ingest_disconnected_at
FROM foghorn.ingest_sessions
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
 AND start_trigger_uuid=sqlc.arg(claim_id)::text
 AND (stream_id=sqlc.arg(stream_id)::uuid OR stream_id IS NULL)
ORDER BY started_at DESC,id LIMIT 1001;

-- name: MarkIngestDisconnected :execrows
UPDATE foghorn.ingest_sessions SET ingest_disconnected_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND id=sqlc.arg(session_id)::uuid
 AND ingest_disconnected_at IS NULL AND ended_at IS NOT NULL
 AND ended_reason='admission_abandoned' AND ingest_disconnect_requested_at IS NOT NULL;

-- name: MarkIngestDisconnectRequested :execrows
UPDATE foghorn.ingest_sessions SET ingest_disconnect_requested_at=COALESCE(ingest_disconnect_requested_at,NOW())
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND id=sqlc.arg(session_id)::uuid
 AND (ended_at IS NULL OR ingest_disconnect_requested_at IS NOT NULL)
 AND ingest_disconnected_at IS NULL;

-- name: FenceIngestStopExecution :execrows
UPDATE foghorn.ingest_sessions SET ingest_disconnect_requested_at=COALESCE(ingest_disconnect_requested_at,NOW())
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND id=sqlc.arg(session_id)::uuid
 AND node_id=sqlc.arg(node_id)::text AND start_trigger_uuid=sqlc.arg(claim_id)::text
 AND stream_internal_name=sqlc.arg(internal_name)::text AND connector_pid=sqlc.arg(connector_pid)::bigint
 AND ingest_disconnected_at IS NULL;
