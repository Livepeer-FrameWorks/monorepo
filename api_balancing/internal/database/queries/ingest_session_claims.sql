-- name: ListActiveIngestSessionClaims :many
SELECT tenant_id::text AS tenant_id, stream_internal_name, node_id,
       COALESCE(ingest_cluster_id, '')::text AS ingest_cluster_id,
       start_trigger_uuid, id::text AS generation
FROM foghorn.ingest_sessions
WHERE ended_at IS NULL;
