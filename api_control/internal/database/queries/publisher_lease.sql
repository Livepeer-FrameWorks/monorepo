-- name: LockPublisherLeaseAuthority :one
SELECT object_version.payload AS object_payload, tenant_version.payload AS tenant_payload,
       object_version.payload_sha256 AS object_digest, tenant_version.payload_sha256 AS tenant_digest,
       object_version.authority_version AS object_version, tenant_version.authority_version AS tenant_version,
       object_version.valid_until AS object_valid_until, tenant_version.valid_until AS tenant_valid_until
FROM commodore.media_authority_current object_current
JOIN commodore.media_authority_versions object_version
  ON object_version.authority_kind=object_current.authority_kind AND object_version.authority_id=object_current.authority_id
 AND object_version.authority_version=object_current.authority_version
JOIN commodore.media_authority_current tenant_current
  ON tenant_current.authority_kind='tenant' AND tenant_current.authority_id=sqlc.arg(tenant_id)::text
JOIN commodore.media_authority_versions tenant_version
  ON tenant_version.authority_kind=tenant_current.authority_kind AND tenant_version.authority_id=tenant_current.authority_id
 AND tenant_version.authority_version=tenant_current.authority_version
WHERE object_current.authority_kind='media_object' AND object_current.authority_id=sqlc.arg(authority_id)
  AND EXISTS (SELECT 1 FROM commodore.media_authority_targets target WHERE target.authority_kind='tenant'
      AND target.authority_id=sqlc.arg(tenant_id)::text AND target.cell_id=sqlc.arg(cell_id))
  AND EXISTS (SELECT 1 FROM commodore.media_authority_targets target WHERE target.authority_kind='media_object'
      AND target.authority_id=object_current.authority_id AND target.cell_id=sqlc.arg(cell_id))
FOR SHARE OF object_current, tenant_current;

-- name: AcquirePublisherLease :one
WITH held AS (
    SELECT s.id, s.active_ingest_cluster_id AS prev_cluster, s.active_ingest_claim_id AS prev_token,
           s.active_ingest_node_id AS prev_node, s.active_ingest_control_cell_id AS prev_cell,
           COALESCE(s.active_ingest_cluster_updated_at >= NOW() - (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second')
             AND s.active_ingest_cluster_id IS NOT NULL AND s.active_ingest_cluster_id <> '', false)::boolean AS prev_fresh
    FROM commodore.streams s JOIN commodore.users u ON u.id=s.user_id AND u.tenant_id=s.tenant_id
    WHERE s.tenant_id=sqlc.arg(tenant_id)::uuid AND s.id=sqlc.arg(stream_id)::uuid
      AND s.internal_name=sqlc.arg(internal_name) AND s.deleted_at IS NULL AND s.ingest_mode='push' AND u.is_active
      AND NOT EXISTS (SELECT 1 FROM commodore.tenant_account_states a WHERE a.tenant_id=s.tenant_id AND a.closed_at IS NOT NULL)
    FOR UPDATE OF s
)
UPDATE commodore.streams s
SET active_ingest_cluster_id=sqlc.arg(cluster_id), active_ingest_claim_id=sqlc.arg(execution_token),
    active_ingest_node_id=sqlc.arg(node_id),active_ingest_control_cell_id=sqlc.arg(control_cell_id),
    active_ingest_cluster_updated_at=NOW(), updated_at=NOW()
FROM held WHERE s.id=held.id AND s.tenant_id=sqlc.arg(tenant_id)::uuid
  AND (NOT held.prev_fresh OR (held.prev_cluster=sqlc.arg(cluster_id) AND held.prev_token=sqlc.arg(execution_token)
    AND ((held.prev_node IS NULL AND held.prev_cell IS NULL) OR (held.prev_node=sqlc.arg(node_id) AND held.prev_cell=sqlc.arg(control_cell_id)))))
RETURNING held.prev_fresh, held.prev_cluster, held.prev_token,
          (s.active_ingest_cluster_updated_at + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'))::timestamptz AS expires_at;

-- name: GetPublisherLease :one
SELECT active_ingest_cluster_id, active_ingest_claim_id,
       COALESCE(active_ingest_cluster_updated_at + (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'), NOW())::timestamptz AS expires_at
FROM commodore.streams
WHERE tenant_id=sqlc.arg(tenant_id)::uuid AND id=sqlc.arg(stream_id)::uuid
  AND internal_name=sqlc.arg(internal_name) AND deleted_at IS NULL;

-- name: LockPublisherLeaseExecution :exec
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(execution_lock_key))::bigint);

-- name: PublisherLeaseAbandoned :one
SELECT EXISTS(SELECT 1 FROM commodore.publisher_lease_abandonments
 WHERE control_cell_id=sqlc.arg(control_cell_id) AND node_id=sqlc.arg(node_id) AND execution_token=sqlc.arg(execution_token));

-- name: RecordPublisherLeaseAbandonment :exec
INSERT INTO commodore.publisher_lease_abandonments(control_cell_id,node_id,execution_token)
VALUES(sqlc.arg(control_cell_id),sqlc.arg(node_id),sqlc.arg(execution_token))
ON CONFLICT(control_cell_id,node_id,execution_token) DO NOTHING;

-- name: ReleaseAbandonedPublisherLease :execrows
-- Execution identity resolves its tenant at the global owner. Only that exact
-- node/cell/token tuple may release it, including before a cell-side row exists.
WITH matched AS (
 SELECT owner.tenant_id,owner.id FROM commodore.streams owner
 WHERE owner.active_ingest_control_cell_id=sqlc.arg(control_cell_id)
   AND owner.active_ingest_node_id=sqlc.arg(node_id) AND owner.active_ingest_claim_id=sqlc.arg(execution_token)
 FOR UPDATE
)
UPDATE commodore.streams s SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,
 active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL,active_ingest_cluster_updated_at=NOW(),updated_at=NOW()
FROM matched WHERE s.tenant_id=matched.tenant_id AND s.id=matched.id;

-- name: PurgePublisherLeaseAbandonments :execrows
WITH expired AS (
 SELECT control_cell_id,node_id,execution_token FROM commodore.publisher_lease_abandonments
 WHERE created_at < NOW() - INTERVAL '2 days' ORDER BY created_at LIMIT 1000
)
DELETE FROM commodore.publisher_lease_abandonments marker USING expired
WHERE marker.control_cell_id=expired.control_cell_id AND marker.node_id=expired.node_id AND marker.execution_token=expired.execution_token;

-- name: LockPublisherLeaseExecutions :exec
-- Sort actual lock IDs before invoking the volatile lock function, so batches
-- with overlapping executions cannot invert their acquisition order.
WITH ordered AS MATERIALIZED (
 SELECT DISTINCT hashtext(execution_key)::bigint AS lock_id
 FROM unnest(sqlc.arg(execution_keys)::text[]) AS executions(execution_key)
 ORDER BY lock_id
)
SELECT pg_advisory_xact_lock(lock_id) FROM ordered;
