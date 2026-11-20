-- name: LockIngestStopTarget :one
SELECT id::text,internal_name,ingest_mode,active_ingest_claim_id,
       COALESCE(NULLIF(active_ingest_control_cell_id,''),active_ingest_cluster_id,'')::text AS control_cell_id
FROM commodore.streams
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND user_id=sqlc.arg(user_id)::uuid
 AND id=sqlc.arg(stream_id)::uuid AND deleted_at IS NULL
FOR UPDATE;

-- name: EnqueueIngestStopObligation :exec
INSERT INTO commodore.media_authority_refresh_obligations(
 target_key,lane,tenant_id,target_kind,last_reason,last_source_service,last_source_event_id,ingest_stop_payload)
VALUES(sqlc.arg(target_key)::text,'ingest_stop',sqlc.arg(tenant_id)::uuid,'live_stream',
 sqlc.arg(reason)::text,'commodore',sqlc.arg(source_event_id)::text,sqlc.arg(payload)::jsonb)
ON CONFLICT(target_key,lane) DO NOTHING;

-- name: GetIngestStopObligationPayload :one
SELECT ingest_stop_payload FROM commodore.media_authority_refresh_obligations
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND target_key=sqlc.arg(target_key)::text AND lane='ingest_stop';

-- name: PurgeCompletedIngestStops :execrows
WITH expired AS (
 SELECT tenant_id,target_key FROM commodore.media_authority_refresh_obligations
 WHERE lane='ingest_stop' AND status='completed' AND updated_at<NOW()-INTERVAL '30 days'
 ORDER BY updated_at,target_key LIMIT 1000
) , removed_claims AS (
 DELETE FROM commodore.media_authority_target_claims c USING expired
 WHERE c.target_key=expired.target_key AND c.lane='ingest_stop' AND c.lease_expires_at<NOW()
 RETURNING c.target_key
)
DELETE FROM commodore.media_authority_refresh_obligations o USING expired
WHERE o.tenant_id=expired.tenant_id AND o.target_key=expired.target_key AND o.lane='ingest_stop';
