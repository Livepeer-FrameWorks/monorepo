-- name: IngestRegistrationCutoff :one
SELECT NOW()::timestamptz AS cutoff;

-- name: ListNodeProjectedIngestSessionsBefore :many
SELECT id::text AS session_id, tenant_id::text AS tenant_id, stream_internal_name
FROM foghorn.ingest_sessions
WHERE node_id = sqlc.arg(node_id) AND ended_at IS NULL AND projection_state = 'active'
  AND started_at < sqlc.arg(started_before)::timestamptz;

-- name: ListNodeIngestGenerations :many
SELECT s.id::text AS session_id, s.tenant_id::text AS tenant_id, s.stream_internal_name,
       (s.ended_at IS NOT NULL)::boolean AS ended, COALESCE(s.ended_reason, '')::text AS ended_reason,
       COALESCE(a.id::text, '')::text AS active_generation, COALESCE(a.node_id, '')::text AS active_node_id
FROM foghorn.ingest_sessions s
LEFT JOIN foghorn.ingest_sessions a
  ON a.tenant_id = s.tenant_id AND a.stream_internal_name = s.stream_internal_name AND a.ended_at IS NULL
WHERE s.node_id = sqlc.arg(node_id) AND s.id = ANY(sqlc.arg(session_ids)::uuid[]);

-- name: EndSupersededNodeIngestSession :one
UPDATE foghorn.ingest_sessions
SET ended_at = NOW(), ended_at_unix_millis = (EXTRACT(EPOCH FROM NOW()) * 1000)::bigint,
    ended_reason = 'superseded_by_new_node'
WHERE id = sqlc.arg(session_id)::text::uuid AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND node_id = sqlc.arg(node_id) AND ended_at IS NULL
RETURNING id::text AS session_id;

-- name: RetireIngestSession :one
UPDATE foghorn.ingest_sessions
SET ended_at = NOW(), ended_at_unix_millis = (EXTRACT(EPOCH FROM NOW()) * 1000)::bigint,
    ended_reason = sqlc.arg(ended_reason)::text
WHERE id = sqlc.arg(session_id)::text::uuid AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND stream_internal_name = sqlc.arg(stream_internal_name) AND ended_at IS NULL
RETURNING node_id, start_trigger_uuid, COALESCE(stream_id::text, '')::text AS stream_id;

-- name: RetireIngestSessionByClaim :one
UPDATE foghorn.ingest_sessions
SET ended_at = NOW(), ended_at_unix_millis = (EXTRACT(EPOCH FROM NOW()) * 1000)::bigint,
    ended_reason = 'placement_claim_lost'
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND stream_internal_name = sqlc.arg(stream_internal_name)
  AND start_trigger_uuid = sqlc.arg(claim_token) AND ended_at IS NULL
RETURNING id::text AS session_id, node_id, COALESCE(stream_id::text, '')::text AS stream_id;

-- name: ListNeverProjectedIngestSessions :many
SELECT id::text AS session_id, tenant_id::text AS tenant_id, stream_internal_name,
       start_trigger_uuid
FROM foghorn.ingest_sessions
WHERE ended_at IS NULL AND projection_state = 'pending'
  AND started_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
ORDER BY started_at
LIMIT 500;

-- name: RetireNeverProjectedIngestSession :one
UPDATE foghorn.ingest_sessions
SET ended_at = NOW(), ended_at_unix_millis = (EXTRACT(EPOCH FROM NOW()) * 1000)::bigint,
    ended_reason = 'projection_timeout'
WHERE id = sqlc.arg(session_id)::text::uuid AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ended_at IS NULL AND projection_state = 'pending'
  AND started_at < NOW() - (sqlc.arg(older_than_ms)::bigint * INTERVAL '1 millisecond')
RETURNING node_id, COALESCE(stream_id::text, '')::text AS stream_id;

-- name: PurgeExpiredCloseTombstones :execrows
DELETE FROM foghorn.ingest_close_tombstones
WHERE created_at < NOW() - make_interval(secs => sqlc.arg(older_than_seconds)::double precision);
