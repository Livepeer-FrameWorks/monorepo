-- name: ListRecentNodeLifecycles :many
SELECT node_id, lifecycle::text AS lifecycle FROM foghorn.node_lifecycle WHERE last_updated > NOW() - INTERVAL '2 minutes' ORDER BY last_updated DESC LIMIT 20;
-- name: LockIngestStream :exec
SELECT pg_advisory_xact_lock(hashtext($1)::bigint);
-- name: CountActiveTenantIngestSessions :one
SELECT COUNT(*)::integer
FROM foghorn.ingest_sessions
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND ended_at IS NULL;
-- name: LockIngestSessionByTrigger :one
SELECT id::text AS id, stream_internal_name, COALESCE(ended_at IS NOT NULL, false)::boolean AS ended, connector_pid FROM foghorn.ingest_sessions
WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND node_id  =  sqlc.arg(node_id) AND start_trigger_uuid  =  sqlc.arg(start_trigger_uuid) FOR UPDATE;
-- name: LockActiveStreamIngestSession :one
SELECT id::text AS id, node_id, connector_pid, started_at_unix_millis, start_trigger_uuid FROM foghorn.ingest_sessions
WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND ended_at IS NULL FOR UPDATE;
-- name: EndSupersededPIDIngestSession :exec
UPDATE foghorn.ingest_sessions SET ended_at  =  NOW(), ended_at_unix_millis  =  sqlc.narg(ended_at_unix_millis), ended_reason  =  'superseded_pid_reuse' WHERE id  =  sqlc.arg(session_id)::uuid AND ended_at IS NULL;
-- name: IngestCloseTombstoneExists :one
SELECT EXISTS (SELECT 1 FROM foghorn.ingest_close_tombstones WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND node_id  =  sqlc.arg(node_id) AND connector_pid  =  sqlc.arg(connector_pid) AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND close_unix_millis >= sqlc.arg(close_unix_millis));
-- name: InsertIngestSession :one
INSERT INTO foghorn.ingest_sessions (tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis, dvr_intent, ingest_cluster_id, projection_state)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(node_id), sqlc.arg(stream_internal_name), sqlc.arg(connector_pid), sqlc.arg(start_trigger_uuid), sqlc.arg(started_at_unix_millis), sqlc.narg(dvr_intent)::jsonb, NULLIF(sqlc.arg(ingest_cluster_id)::text, ''), 'pending') RETURNING id::text;
-- name: InsertIngestSessionWithAuthority :one
INSERT INTO foghorn.ingest_sessions
    (tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid,
     started_at_unix_millis, dvr_intent, ingest_cluster_id, projection_state,
     media_authority_id, media_authority_version, tenant_authority_version, processes_json,
     capacity_max_streams)
VALUES
    (sqlc.arg(tenant_id)::uuid, sqlc.arg(node_id), sqlc.arg(stream_internal_name),
     sqlc.arg(connector_pid), sqlc.arg(start_trigger_uuid), sqlc.arg(started_at_unix_millis),
     sqlc.narg(dvr_intent)::jsonb, NULLIF(sqlc.arg(ingest_cluster_id)::text, ''), 'pending',
     sqlc.arg(media_authority_id), sqlc.arg(media_authority_version),
     sqlc.arg(tenant_authority_version), sqlc.arg(processes_json), sqlc.arg(capacity_max_streams))
RETURNING id::text;
-- name: GetIngestSessionAuthoritySnapshot :one
SELECT COALESCE(media_authority_id, '')::text AS media_authority_id,
       COALESCE(media_authority_version, 0)::bigint AS media_authority_version,
       COALESCE(tenant_authority_version, 0)::bigint AS tenant_authority_version,
       processes_json
FROM foghorn.ingest_sessions
WHERE id = sqlc.arg(session_id)::uuid;
-- name: ClaimDVRStopsForGeneration :many
UPDATE foghorn.artifacts SET dvr_start_dispatch = jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{state}', '"stop_pending"'::jsonb), status = 'stopping', updated_at = NOW()
WHERE artifact_type  =  'dvr' AND status IN ('requested', 'starting', 'recording') AND ingest_generation  =  sqlc.arg(ingest_generation)::uuid AND tenant_id::text  =  sqlc.arg(tenant_id)
RETURNING artifact_hash, COALESCE(dvr_start_dispatch->>'node_id', '')::text AS storage_node_id;
-- name: ClaimDVRStopByArtifact :many
UPDATE foghorn.artifacts
SET dvr_start_dispatch = jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{state}', '"stop_pending"'::jsonb),
    status = 'stopping', updated_at = NOW()
WHERE artifact_type = 'dvr'
  AND status IN ('requested', 'starting', 'recording')
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING artifact_hash, COALESCE(dvr_start_dispatch->>'node_id', '')::text AS storage_node_id;
-- name: ClaimDVRStopsForEndedSource :many
UPDATE foghorn.artifacts
SET dvr_start_dispatch  =  jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{state}', '"stop_pending"'::jsonb),
    status  =  'stopping', updated_at  =  NOW()
WHERE foghorn.artifacts.artifact_type  =  'dvr'
  AND foghorn.artifacts.status IN ('requested', 'starting', 'recording')
  AND foghorn.artifacts.stream_internal_name  =  $1
  AND foghorn.artifacts.dvr_start_dispatch->>'source_node_id'  =  $2
  AND foghorn.artifacts.tenant_id::text  =  $3
  AND (foghorn.artifacts.ingest_generation IS NULL OR NOT EXISTS (
      SELECT 1 FROM foghorn.ingest_sessions s
      WHERE s.id  =  foghorn.artifacts.ingest_generation AND s.ended_at IS NULL
  ))
RETURNING artifact_hash, COALESCE(dvr_start_dispatch->>'node_id', '')::text AS storage_node_id;
-- name: GetOpenIngestSessionCluster :one
SELECT COALESCE(ingest_cluster_id, '')::text FROM foghorn.ingest_sessions WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND node_id  =  sqlc.arg(node_id) AND start_trigger_uuid  =  sqlc.arg(start_trigger_uuid) AND ended_at IS NULL;
-- name: ReapStreamEndIngestSessions :many
UPDATE foghorn.ingest_sessions SET ended_at  =  NOW(), ended_at_unix_millis  =  sqlc.narg(ended_at_unix_millis), ended_reason  =  'stream_end_reaped'
WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND node_id  =  sqlc.arg(node_id) AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND ended_at IS NULL AND started_at_unix_millis <= sqlc.narg(ended_at_unix_millis)
RETURNING id::text AS session_id, start_trigger_uuid;
-- name: ReapExactMissingIngestSession :one
UPDATE foghorn.ingest_sessions
SET ended_at = NOW(),
    ended_at_unix_millis = sqlc.narg(ended_at_unix_millis),
    ended_reason = 'runtime_absent'
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND node_id = sqlc.arg(node_id)
  AND stream_internal_name = sqlc.arg(stream_internal_name)
  AND id = sqlc.arg(generation)::uuid
  AND connector_pid = sqlc.arg(connector_pid)
  AND ended_at IS NULL
RETURNING id::text AS session_id, start_trigger_uuid;
-- name: HasActiveStreamIngestSession :one
SELECT EXISTS (SELECT 1 FROM foghorn.ingest_sessions WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND ended_at IS NULL);
-- name: ProbeCurrentSourceProjection :one
SELECT EXISTS (SELECT 1 FROM foghorn.ingest_sessions s WHERE s.tenant_id  =  sqlc.arg(tenant_id)::uuid AND s.stream_internal_name  =  sqlc.arg(stream_internal_name) AND s.id  =  sqlc.arg(generation)::uuid AND s.ended_at IS NULL) AS is_current,
(SELECT s.source_revision FROM foghorn.ingest_sessions s WHERE s.id  =  sqlc.arg(generation)::uuid AND s.tenant_id  =  sqlc.arg(tenant_id)::uuid) AS source_revision,
COALESCE((SELECT s.projection_state FROM foghorn.ingest_sessions s WHERE s.id  =  sqlc.arg(generation)::uuid AND s.tenant_id  =  sqlc.arg(tenant_id)::uuid), '')::text AS projection_state;
-- name: PersistSourceProjectionRevision :exec
UPDATE foghorn.ingest_sessions SET source_revision  =  sqlc.narg(source_revision) WHERE id  =  sqlc.arg(generation)::uuid AND ended_at IS NULL AND projection_state  =  'pending';
-- name: AdvanceActiveSourceProjectionRevision :execrows
UPDATE foghorn.ingest_sessions
SET source_revision = sqlc.arg(new_revision)
WHERE id = sqlc.arg(generation)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND stream_internal_name = sqlc.arg(stream_internal_name)
  AND ended_at IS NULL
  AND projection_state = 'active'
  AND source_revision = sqlc.arg(previous_revision);
-- name: AdvanceAdmissionEffectSourceRevision :execrows
UPDATE foghorn.ingest_admission_effects
SET source_revision = sqlc.arg(new_revision), updated_at = NOW()
WHERE source_generation = sqlc.arg(generation)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND stream_internal_name = sqlc.arg(stream_internal_name)
  AND source_revision = sqlc.arg(previous_revision);
-- name: ConfirmSourceProjection :execrows
UPDATE foghorn.ingest_sessions SET projection_state  =  'active', projected_at  =  COALESCE(projected_at, NOW())
WHERE id  =  sqlc.arg(generation)::uuid AND tenant_id  =  sqlc.arg(tenant_id)::uuid AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND ended_at IS NULL AND source_revision  =  sqlc.narg(source_revision) AND projection_state  =  'pending';
-- name: AbortPendingSourceProjection :one
UPDATE foghorn.ingest_sessions SET ended_at  =  NOW(), ended_at_unix_millis  =  (EXTRACT(EPOCH FROM NOW()) * 1000)::bigint, ended_reason  =  'projection_failed'
WHERE id  =  sqlc.arg(generation)::uuid AND tenant_id  =  sqlc.arg(tenant_id)::uuid AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND ended_at IS NULL AND projection_state  =  'pending'
RETURNING node_id, start_trigger_uuid;
-- name: NextSourceProjectionRevision :one
-- The database counter includes tenant_id because that is the durable ownership domain.
-- Commodore guarantees stream_internal_name is globally unique, which is why the corresponding
-- Redis source projection can remain keyed by internal name alone.
INSERT INTO foghorn.source_projection_revision_counter (tenant_id, stream_internal_name, value)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(stream_internal_name), 4503599627370497)
ON CONFLICT (tenant_id, stream_internal_name) DO UPDATE
SET value = foghorn.source_projection_revision_counter.value + 1
RETURNING value AS revision;
-- name: CloseIngestSession :one
UPDATE foghorn.ingest_sessions SET ended_at  =  NOW(), ended_at_unix_millis  =  sqlc.narg(close_unix_millis), ended_reason  =  'push_input_close'
WHERE tenant_id  =  sqlc.arg(tenant_id)::uuid AND node_id  =  sqlc.arg(node_id) AND connector_pid  =  sqlc.arg(connector_pid) AND ended_at IS NULL AND stream_internal_name  =  sqlc.arg(stream_internal_name) AND started_at_unix_millis <= sqlc.narg(close_unix_millis)
RETURNING id::text AS id, start_trigger_uuid, COALESCE(ingest_cluster_id, '')::text AS cluster_id;
-- name: InsertIngestCloseTombstone :exec
INSERT INTO foghorn.ingest_close_tombstones (tenant_id, node_id, connector_pid, stream_internal_name, close_unix_millis) VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(node_id), sqlc.arg(connector_pid), sqlc.arg(stream_internal_name), sqlc.arg(close_unix_millis));
-- name: ClaimUnstartedDVRIntents :many
WITH claimed AS (SELECT s.id FROM foghorn.ingest_sessions s WHERE s.dvr_intent IS NOT NULL AND s.ended_at IS NULL AND s.dvr_intent_error IS NULL
AND (s.dvr_intent_lease_until IS NULL OR s.dvr_intent_lease_until<NOW()) AND s.started_at<NOW()-(sqlc.arg(grace_seconds)::bigint*INTERVAL '1 second')
AND NOT EXISTS (SELECT 1 FROM foghorn.artifacts a WHERE a.ingest_generation = s.id AND a.artifact_type = 'dvr') ORDER BY s.started_at FOR UPDATE SKIP LOCKED LIMIT sqlc.arg(batch_limit))
UPDATE foghorn.ingest_sessions u SET dvr_intent_attempts = u.dvr_intent_attempts+1, dvr_intent_lease_until = NOW()+(sqlc.arg(lease_seconds)::bigint*INTERVAL '1 second') FROM claimed WHERE u.id = claimed.id
RETURNING u.id::text AS id, u.tenant_id::text AS tenant_id, u.stream_internal_name, u.node_id, u.dvr_intent::text AS dvr_intent, u.dvr_intent_attempts;
-- name: FailDVRIntent :exec
UPDATE foghorn.ingest_sessions SET dvr_intent_error  =  sqlc.narg(dvr_intent_error), dvr_intent_lease_until  =  NULL WHERE id  =  sqlc.arg(session_id)::uuid AND tenant_id  =  sqlc.arg(tenant_id)::uuid AND dvr_intent_error IS NULL;
