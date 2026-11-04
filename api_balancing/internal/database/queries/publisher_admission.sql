-- name: PreparePublisherAdmission :one
SELECT foghorn.prepare_publisher_admission_v1(sqlc.arg(request)::jsonb)::jsonb AS result;

-- name: ConfirmPublisherAdmission :one
SELECT foghorn.confirm_publisher_admission_v1(sqlc.arg(request)::jsonb)::boolean AS confirmed;

-- name: GetPublisherAdmission :one
SELECT id::text AS generation, source_revision, projection_state, ended_reason, ended_at,
       stream_internal_name, connector_pid, publisher_admission_version, COALESCE(stream_id::text, '')::text AS stream_id
FROM foghorn.ingest_sessions
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND node_id=sqlc.arg(node_id) AND start_trigger_uuid=sqlc.arg(trigger_uuid);

-- name: ListPublisherAdmissionsToSettle :many
-- Local marker completion is independent of global lease forwarding. Bound the
-- marker work before joining sessions, including executions that never minted.
SELECT generation, tenant_id, stream_internal_name, node_id, start_trigger_uuid,
       publisher_admission_version, stream_id, reason
FROM (
  (SELECT COALESCE(session.id::text,'')::text AS generation, COALESCE(session.tenant_id::text,'')::text AS tenant_id,
          COALESCE(session.stream_internal_name,'')::text AS stream_internal_name,
          abandoned.node_id, abandoned.start_trigger_uuid,
          COALESCE(session.publisher_admission_version,0)::smallint AS publisher_admission_version,
          COALESCE(session.stream_id::text,'')::text AS stream_id, 'admission_abandoned'::text AS reason,
          COALESCE(session.started_at,abandoned.created_at) AS started_at
   FROM (
     SELECT node_id,start_trigger_uuid,created_at FROM foghorn.ingest_admission_abandonments
     WHERE publisher_sessions_settled_at IS NULL ORDER BY created_at LIMIT 32
   ) abandoned
   LEFT JOIN LATERAL (
     SELECT candidate.* FROM foghorn.ingest_sessions candidate
     WHERE candidate.node_id=abandoned.node_id AND candidate.start_trigger_uuid=abandoned.start_trigger_uuid
       AND candidate.ended_at IS NULL
     LIMIT 32
   ) session ON TRUE
   LIMIT 32)
  UNION ALL
  (SELECT session.id::text, session.tenant_id::text, session.stream_internal_name,
          session.node_id, session.start_trigger_uuid, session.publisher_admission_version,
          COALESCE(session.stream_id::text, '')::text, 'admission_lapsed'::text, session.started_at
   FROM foghorn.ingest_sessions session
   WHERE session.ended_at IS NULL AND session.publisher_admission_version=1 AND session.projection_state='pending'
     AND session.started_at < NOW() - (sqlc.arg(admission_window_ms)::bigint * INTERVAL '1 millisecond')
     AND NOT EXISTS (SELECT 1 FROM foghorn.ingest_admission_abandonments abandoned
       WHERE abandoned.node_id=session.node_id AND abandoned.start_trigger_uuid=session.start_trigger_uuid)
   ORDER BY session.started_at LIMIT 32)
) candidates
ORDER BY started_at LIMIT 32;

-- name: MarkPublisherSessionsSettled :execrows
-- The durable marker prevents a later mint; keep local work queued while any
-- previously committed generation for this node execution is still open.
UPDATE foghorn.ingest_admission_abandonments marker SET publisher_sessions_settled_at=NOW()
WHERE marker.node_id=sqlc.arg(node_id) AND marker.start_trigger_uuid=sqlc.arg(trigger_uuid)
  AND marker.publisher_sessions_settled_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM foghorn.ingest_sessions session
    WHERE session.node_id=marker.node_id AND session.start_trigger_uuid=marker.start_trigger_uuid AND session.ended_at IS NULL);

-- name: ListPublisherLeaseReleases :many
SELECT id::text AS generation, tenant_id::text AS tenant_id, stream_internal_name, ingest_cluster_id, start_trigger_uuid, node_id
FROM foghorn.ingest_sessions
WHERE publisher_admission_version=1 AND ended_at IS NOT NULL AND publisher_lease_released_at IS NULL
ORDER BY ended_at LIMIT 100;

-- name: PublisherAdmissionStillLapsed :one
SELECT EXISTS(SELECT 1 FROM foghorn.ingest_sessions
 WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND id=sqlc.arg(generation)::uuid
   AND ended_at IS NULL AND projection_state='pending'
   AND started_at < NOW() - (sqlc.arg(admission_window_ms)::bigint * INTERVAL '1 millisecond'));

-- name: SettlePublisherAdmission :one
SELECT foghorn.settle_publisher_admission_v1(sqlc.arg(request)::jsonb)::boolean AS settled;

-- name: MarkPublisherLeasesReleased :execrows
UPDATE foghorn.ingest_sessions session SET publisher_lease_released_at=NOW()
FROM jsonb_to_recordset(sqlc.arg(releases)::jsonb) AS released(tenant_id text,generation text)
WHERE session.tenant_id=released.tenant_id::uuid AND session.id=released.generation::uuid AND session.ended_at IS NOT NULL;

-- name: ListPublisherAbandonmentsToForward :many
-- Execution markers precede tenant discovery and are scoped to a node.
SELECT node_id,start_trigger_uuid FROM foghorn.ingest_admission_abandonments
WHERE publisher_lease_settled_at IS NULL ORDER BY created_at LIMIT 32;

-- name: MarkPublisherAbandonmentForwarded :execrows
UPDATE foghorn.ingest_admission_abandonments SET publisher_lease_settled_at=NOW()
WHERE node_id=sqlc.arg(node_id) AND start_trigger_uuid=sqlc.arg(trigger_uuid);

-- name: ListEndedPublisherClaimNodes :many
SELECT DISTINCT node_id
FROM foghorn.ingest_sessions
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND stream_internal_name=sqlc.arg(internal_name)
  AND ingest_cluster_id=sqlc.arg(cluster_id) AND start_trigger_uuid=sqlc.arg(claim_token)
  AND publisher_admission_version=1 AND ended_at IS NOT NULL
ORDER BY node_id;

-- name: ListLegacyPublisherClaimReleasesByExecution :many
-- A node execution's abandonment precedes tenant discovery. Every returned
-- legacy owner is released with its own tenant, stream, cluster and claim.
SELECT tenant_id::text AS tenant_id,stream_internal_name,ingest_cluster_id,start_trigger_uuid,(ended_at IS NOT NULL)::boolean AS ended
FROM foghorn.ingest_sessions
WHERE node_id=sqlc.arg(node_id) AND start_trigger_uuid=sqlc.arg(trigger_uuid)
  AND publisher_admission_version=0;

-- name: ListDVRStopClaimsByGeneration :many
SELECT artifact_hash,COALESCE(dvr_start_dispatch->>'node_id','')::text AS storage_node_id
FROM foghorn.artifacts
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND ingest_generation=sqlc.arg(generation)::uuid
  AND artifact_type='dvr' AND dvr_start_dispatch->>'state'='stop_pending';
