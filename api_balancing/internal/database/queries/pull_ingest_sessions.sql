-- name: AdmitPullUpstream :one
SELECT foghorn.admit_pull_ingest_v1(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(stream_id)::text::uuid,
    sqlc.arg(internal_name)::text,sqlc.arg(node_id)::text,sqlc.arg(cluster_id)::text,sqlc.arg(max_streams)::integer) AS result;

-- name: PullSessionForBuffer :one
SELECT s.id::text AS id,(s.ended_at IS NOT NULL)::boolean AS ended,
    COALESCE(s.start_event_id,'')::text AS start_event_id,
    COALESCE(s.playable_at_unix_millis,0)::bigint AS playable_at_unix_millis,
    CASE WHEN EXISTS (
        SELECT 1 FROM foghorn.pull_ingest_sessions pending
        WHERE pending.tenant_id=s.tenant_id AND pending.stream_internal_name=s.stream_internal_name
          AND pending.node_id=s.node_id AND pending.ended_at IS NULL AND pending.admission_state='pending'
          AND pending.started_at>s.ended_at
    ) THEN TRUE ELSE FALSE END::boolean AS has_new_admission
FROM foghorn.pull_ingest_sessions s
WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid AND s.stream_internal_name=sqlc.arg(internal_name)::text
  AND s.node_id=sqlc.arg(node_id)::text AND s.buffer_pid=sqlc.arg(buffer_pid)
ORDER BY s.started_at DESC,s.id DESC LIMIT 1;

-- name: LatestPullNodeEnd :one
SELECT COALESCE(MAX(ended_at_unix_millis),0)::bigint AS ended_at_unix_millis
FROM foghorn.pull_ingest_sessions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
  AND node_id=sqlc.arg(node_id)::text AND ended_at IS NOT NULL;

-- name: CountActivePullStreamSessions :one
SELECT count(*)::integer FROM foghorn.pull_ingest_sessions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
  AND ended_at IS NULL AND admission_state='active';

-- name: EndSupersededPullBuffers :many
UPDATE foghorn.pull_ingest_sessions SET ended_at=NOW(),ended_at_unix_millis=sqlc.arg(event_millis)::bigint,ended_reason='superseded_buffer'
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
  AND node_id=sqlc.arg(node_id)::text AND ended_at IS NULL AND admission_state='active'
  AND (buffer_pid<>sqlc.arg(buffer_pid)::bigint OR
    (sqlc.arg(new_buffer_full)::boolean AND start_event_id<>sqlc.arg(event_id)::text))
  AND playable_at_unix_millis < sqlc.arg(event_millis)::bigint
RETURNING id::text AS id,stream_id::text AS stream_id,cell_session_id::text AS cell_session_id;

-- name: BindPendingPullBuffer :one
UPDATE foghorn.pull_ingest_sessions AS target
SET admission_state='active',buffer_pid=sqlc.arg(buffer_pid)::bigint,start_event_id=sqlc.arg(event_id)::text,
    cell_session_id=COALESCE((SELECT active.cell_session_id FROM foghorn.pull_ingest_sessions active
      WHERE active.tenant_id=sqlc.arg(tenant_id)::text::uuid AND active.stream_internal_name=sqlc.arg(internal_name)::text
        AND active.ended_at IS NULL AND active.admission_state='active'
      ORDER BY active.started_at,active.id LIMIT 1),gen_random_uuid()),
    started_at=to_timestamp(sqlc.arg(event_millis)::bigint/1000.0),started_at_unix_millis=sqlc.arg(event_millis)::bigint,
    playable_at=NOW(),playable_at_unix_millis=sqlc.arg(event_millis)::bigint
WHERE target.tenant_id=sqlc.arg(tenant_id)::text::uuid AND target.stream_internal_name=sqlc.arg(internal_name)::text
  AND target.node_id=sqlc.arg(node_id)::text AND target.admission_state='pending' AND target.ended_at IS NULL
RETURNING target.cell_session_id::text AS id;

-- name: InsertPlayablePullBuffer :one
INSERT INTO foghorn.pull_ingest_sessions(tenant_id,stream_id,stream_internal_name,node_id,ingest_cluster_id,
    admission_state,buffer_pid,start_event_id,started_at,started_at_unix_millis,playable_at,playable_at_unix_millis,cell_session_id)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(stream_id)::text::uuid,sqlc.arg(internal_name)::text,sqlc.arg(node_id)::text,sqlc.arg(cluster_id)::text,
    'active',sqlc.arg(buffer_pid)::bigint,sqlc.arg(event_id)::text,to_timestamp(sqlc.arg(event_millis)::bigint/1000.0),
    sqlc.arg(event_millis)::bigint,NOW(),sqlc.arg(event_millis)::bigint,
    COALESCE((SELECT s.cell_session_id FROM foghorn.pull_ingest_sessions s
      WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid AND s.stream_internal_name=sqlc.arg(internal_name)::text AND s.ended_at IS NULL AND s.admission_state='active'
      ORDER BY s.started_at,s.id LIMIT 1),gen_random_uuid()))
RETURNING cell_session_id::text AS id;

-- name: EndPullBuffersForEvent :many
UPDATE foghorn.pull_ingest_sessions
SET ended_at=NOW(),ended_at_unix_millis=sqlc.arg(event_millis)::bigint,ended_reason='stream_end'
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
  AND node_id=sqlc.arg(node_id)::text AND ended_at IS NULL AND admission_state='active'
  AND playable_at_unix_millis < sqlc.arg(event_millis)::bigint
RETURNING id::text AS id,stream_id::text AS stream_id,cell_session_id::text AS cell_session_id;

-- name: RetirePullIngestSession :one
UPDATE foghorn.pull_ingest_sessions
SET ended_at=NOW(),ended_at_unix_millis=sqlc.arg(event_millis)::bigint,ended_reason=sqlc.arg(reason)
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND stream_internal_name=sqlc.arg(internal_name)::text
  AND id=sqlc.arg(session_id)::text::uuid AND ended_at IS NULL
  AND (NOT sqlc.arg(pending_only)::boolean OR (admission_state='pending' AND started_at<NOW()-INTERVAL '60 seconds'))
RETURNING stream_id::text AS stream_id,node_id,admission_state,cell_session_id::text AS cell_session_id;

-- name: ListNodeOpenPullSessionsBefore :many
-- Node-scoped registration reconciliation; returned tenant scopes each retirement.
SELECT id::text AS id,tenant_id::text AS tenant_id,stream_internal_name,buffer_pid
FROM foghorn.pull_ingest_sessions
WHERE node_id=sqlc.arg(node_id)::text AND ended_at IS NULL AND admission_state='active'
  AND started_at_unix_millis>0 AND started_at_unix_millis < sqlc.arg(cutoff_millis)::bigint;

-- name: ListLapsedPullAdmissions :many
-- Cell-scoped maintenance; each candidate carries the tenant used by the retirement transaction.
SELECT id::text AS id,tenant_id::text AS tenant_id,stream_internal_name FROM foghorn.pull_ingest_sessions
WHERE ended_at IS NULL AND admission_state='pending' AND started_at<NOW()-INTERVAL '60 seconds'
ORDER BY started_at,id LIMIT 128;

-- name: PurgeEndedPullIngestSessions :execrows
WITH old_ended AS (
    SELECT id FROM foghorn.pull_ingest_sessions
    WHERE ended_at<NOW()-sqlc.arg(retention_seconds)::bigint*INTERVAL '1 second'
    ORDER BY ended_at,id LIMIT sqlc.arg(batch_limit)
)
DELETE FROM foghorn.pull_ingest_sessions s USING old_ended o WHERE s.id=o.id;
