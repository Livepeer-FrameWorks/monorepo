-- name: ListActiveIngestSessionClaims :many
-- A pending session holds its claim only while its admission can still be answered; past the
-- admission window Mist has refused that push, so the claim is left to lapse.
SELECT tenant_id::text AS tenant_id, stream_internal_name, node_id,
       COALESCE(ingest_cluster_id, '')::text AS ingest_cluster_id,
       start_trigger_uuid, id::text AS generation
FROM foghorn.ingest_sessions
WHERE ended_at IS NULL
  AND (projection_state = 'active'
       OR started_at >= NOW() - (sqlc.arg(admission_window_ms)::bigint * INTERVAL '1 millisecond'));
