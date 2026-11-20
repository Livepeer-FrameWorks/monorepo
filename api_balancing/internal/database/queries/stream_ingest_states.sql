-- name: GetStreamIngestStates :many
SELECT DISTINCT ON (stream_id) stream_id, id, started_at, playable_at, ended_at, is_pull
FROM (
  SELECT stream_id, id, started_at, playable_at, ended_at, false::boolean AS is_pull
  FROM foghorn.ingest_sessions
  WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    AND stream_id = ANY(sqlc.arg(stream_ids)::uuid[]) AND projection_state = 'active'
  UNION ALL
  SELECT stream_id, cell_session_id AS id, MIN(started_at)::timestamptz AS started_at,
         MIN(playable_at)::timestamptz AS playable_at,
         CASE WHEN bool_or(ended_at IS NULL) THEN NULL ELSE MAX(ended_at) END::timestamptz AS ended_at,
         true::boolean AS is_pull
  FROM foghorn.pull_ingest_sessions
  WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    AND stream_id = ANY(sqlc.arg(stream_ids)::uuid[]) AND admission_state = 'active'
  GROUP BY stream_id, cell_session_id
) sessions
ORDER BY stream_id,
  CASE WHEN ended_at IS NULL AND playable_at IS NOT NULL THEN 3
       WHEN ended_at IS NULL THEN 2 ELSE 1 END DESC,
  started_at DESC, id DESC;

-- name: MarkPushBufferEnded :one
WITH candidate AS (
  SELECT s.id,s.started_at FROM foghorn.ingest_sessions s
  WHERE s.tenant_id=sqlc.arg(tenant_id)::uuid AND s.stream_internal_name=sqlc.arg(internal_name)
    AND s.node_id=sqlc.arg(node_id) AND s.playable_at_unix_millis<sqlc.arg(event_millis)::bigint
  ORDER BY s.started_at DESC,s.id DESC LIMIT 1
)
UPDATE foghorn.ingest_sessions target SET buffer_ended_at=NOW()
FROM candidate c
WHERE target.tenant_id=sqlc.arg(tenant_id)::uuid AND target.id=c.id
  AND target.ended_at IS NOT NULL AND target.buffer_ended_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.ingest_sessions newer
    WHERE newer.tenant_id=target.tenant_id AND newer.stream_internal_name=target.stream_internal_name
      AND (newer.started_at,newer.id)>(c.started_at,c.id)
  )
RETURNING target.id::text AS session_id,COALESCE(target.stream_id::text,'')::text AS stream_id;

-- name: MarkPullBufferEnded :one
WITH candidate AS (
  SELECT s.id,s.cell_session_id,s.started_at FROM foghorn.pull_ingest_sessions s
  WHERE s.tenant_id=sqlc.arg(tenant_id)::uuid AND s.stream_internal_name=sqlc.arg(internal_name)
    AND s.node_id=sqlc.arg(node_id) AND s.playable_at_unix_millis<sqlc.arg(event_millis)::bigint
  ORDER BY s.started_at DESC,s.id DESC LIMIT 1
)
UPDATE foghorn.pull_ingest_sessions target SET buffer_ended_at=NOW()
FROM candidate c
WHERE target.tenant_id=sqlc.arg(tenant_id)::uuid AND target.cell_session_id=c.cell_session_id
  AND target.id=(SELECT first_row.id FROM foghorn.pull_ingest_sessions first_row
    WHERE first_row.tenant_id=target.tenant_id AND first_row.cell_session_id=c.cell_session_id
    ORDER BY first_row.started_at,first_row.id LIMIT 1)
  AND target.buffer_ended_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.pull_ingest_sessions active
    WHERE active.tenant_id=target.tenant_id AND active.stream_internal_name=target.stream_internal_name
      AND active.ended_at IS NULL AND active.admission_state='active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM foghorn.pull_ingest_sessions newer
    WHERE newer.tenant_id=target.tenant_id AND newer.stream_internal_name=target.stream_internal_name
      AND newer.admission_state='active' AND newer.cell_session_id<>c.cell_session_id
      AND (newer.started_at,newer.id)>(c.started_at,c.id)
  )
RETURNING target.cell_session_id::text AS session_id,target.stream_id::text AS stream_id;
