-- name: ListActiveIngestSessionClaims :many
-- A pending session holds its claim only while its admission can still be answered; past the
-- admission window Mist has refused that push, so the claim is left to lapse.
-- projection_state is either 'active' or 'pending', so the two branches are disjoint and each
-- reads only open sessions through its own partial index.
SELECT tenant_id::text AS tenant_id, stream_internal_name, node_id,
       COALESCE(ingest_cluster_id, '')::text AS ingest_cluster_id,
       start_trigger_uuid, id::text AS generation
FROM foghorn.ingest_sessions
WHERE ended_at IS NULL AND projection_state = 'active'
UNION ALL
SELECT tenant_id::text AS tenant_id, stream_internal_name, node_id,
       COALESCE(ingest_cluster_id, '')::text AS ingest_cluster_id,
       start_trigger_uuid, id::text AS generation
FROM foghorn.ingest_sessions
WHERE ended_at IS NULL AND projection_state = 'pending'
  AND started_at >= NOW() - (sqlc.arg(admission_window_ms)::bigint * INTERVAL '1 millisecond');
