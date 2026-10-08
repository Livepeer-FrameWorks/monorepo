-- name: GetClusterOwnerAndName :one
SELECT owner_tenant_id, cluster_name,
       COALESCE(cluster_class, '')::text AS cluster_class,
       is_platform_official, visibility
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: GetClusterOwner :one
SELECT owner_tenant_id
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: GetTenantName :one
SELECT name FROM quartermaster.tenants WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: FindPendingClusterInviteID :one
SELECT id FROM quartermaster.cluster_invites
WHERE cluster_id = sqlc.arg(cluster_id)
  AND invited_tenant_id = sqlc.arg(invited_tenant_id)::uuid
  AND status = 'pending';

-- name: CreateClusterInviteRecord :exec
INSERT INTO quartermaster.cluster_invites (
    id, cluster_id, invited_tenant_id, invite_token, access_level,
    resource_limits, status, created_by, created_at, expires_at
) VALUES (
    sqlc.arg(id)::uuid, sqlc.arg(cluster_id), sqlc.arg(invited_tenant_id)::uuid,
    sqlc.arg(invite_token), sqlc.arg(access_level), sqlc.narg(resource_limits)::text::jsonb,
    'pending', sqlc.arg(created_by)::uuid, sqlc.arg(created_at), sqlc.arg(expires_at)
);

-- name: GetClusterInviteOwner :one
SELECT i.cluster_id, c.owner_tenant_id
FROM quartermaster.cluster_invites i
JOIN quartermaster.infrastructure_clusters c ON i.cluster_id = c.cluster_id
WHERE i.id = sqlc.arg(invite_id)::uuid;

-- name: RevokeClusterInviteRecord :exec
UPDATE quartermaster.cluster_invites SET status = 'revoked'
WHERE id = sqlc.arg(invite_id)::uuid;

-- name: GetActiveClusterSubscriptionPolicy :one
SELECT visibility, pricing_model, requires_approval, owner_tenant_id,
       COALESCE(cluster_class, '')::text AS cluster_class,
       is_platform_official
FROM quartermaster.infrastructure_clusters
WHERE cluster_id = sqlc.arg(cluster_id) AND is_active = true;

-- name: GetPendingInviteByToken :one
SELECT id, cluster_id, invited_tenant_id, access_level, resource_limits
FROM quartermaster.cluster_invites
WHERE invite_token = sqlc.arg(invite_token) AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > NOW());

-- name: GetPendingInviteWithClusterPolicy :one
SELECT i.id, i.cluster_id, i.invited_tenant_id, i.access_level, i.resource_limits,
       c.pricing_model, c.owner_tenant_id,
       COALESCE(c.cluster_class, '')::text AS cluster_class,
       c.is_platform_official, c.visibility
FROM quartermaster.cluster_invites i
JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = i.cluster_id
WHERE i.invite_token = sqlc.arg(invite_token) AND i.status = 'pending'
  AND (i.expires_at IS NULL OR i.expires_at > NOW());

-- name: AcceptClusterInviteRecord :exec
UPDATE quartermaster.cluster_invites
SET status = 'accepted', accepted_at = NOW()
WHERE id = sqlc.arg(invite_id)::uuid;

-- name: UpsertRequestedClusterSubscription :one
INSERT INTO quartermaster.tenant_cluster_access (
    id, tenant_id, cluster_id, access_level, access_source, subscription_status,
    resource_limits, requested_at, is_active, created_at, updated_at
) VALUES (
    sqlc.arg(id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(access_level),
    sqlc.arg(access_source),
    sqlc.arg(subscription_status), sqlc.narg(resource_limits)::text::jsonb,
    sqlc.arg(requested_at), true, sqlc.arg(requested_at), sqlc.arg(requested_at)
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    access_source = EXCLUDED.access_source,
    subscription_status = EXCLUDED.subscription_status,
    resource_limits = COALESCE(EXCLUDED.resource_limits, quartermaster.tenant_cluster_access.resource_limits),
    requested_at = COALESCE(quartermaster.tenant_cluster_access.requested_at, EXCLUDED.requested_at),
    is_active = true,
    updated_at = NOW()
RETURNING id;

-- name: UpsertAcceptedClusterSubscription :one
INSERT INTO quartermaster.tenant_cluster_access (
    id, tenant_id, cluster_id, access_level, access_source, subscription_status,
    resource_limits, approved_at, is_active, created_at, updated_at
) VALUES (
    sqlc.arg(id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(access_level),
    'private_invite',
    'active', sqlc.narg(resource_limits)::text::jsonb, sqlc.arg(approved_at), true,
    sqlc.arg(approved_at), sqlc.arg(approved_at)
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    access_source = 'private_invite',
    subscription_status = 'active',
    resource_limits = COALESCE(EXCLUDED.resource_limits, quartermaster.tenant_cluster_access.resource_limits),
    approved_at = NOW(), is_active = true, updated_at = NOW()
RETURNING id;

-- name: GetSubscriptionOwnerPolicy :one
SELECT a.tenant_id, a.cluster_id, a.access_source, a.subscription_status,
       c.owner_tenant_id, c.pricing_model, c.is_platform_official
FROM quartermaster.tenant_cluster_access a
JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
WHERE a.id = sqlc.arg(subscription_id)::uuid
FOR UPDATE OF a;

-- name: GetSubscriptionOwner :one
SELECT a.tenant_id, a.cluster_id, c.owner_tenant_id
FROM quartermaster.tenant_cluster_access a
JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
WHERE a.id = sqlc.arg(subscription_id)::uuid;

-- name: ApproveClusterSubscriptionRecord :exec
UPDATE quartermaster.tenant_cluster_access
SET subscription_status = 'active', approved_at = NOW(),
    approved_by = sqlc.arg(approved_by)::uuid, is_active = true, granted_at = NOW(),
    updated_at = NOW()
WHERE id = sqlc.arg(subscription_id)::uuid
  AND subscription_status = 'pending_approval';

-- name: RejectClusterSubscriptionRecord :exec
UPDATE quartermaster.tenant_cluster_access
SET subscription_status = 'rejected', rejection_reason = sqlc.arg(rejection_reason),
    is_active = false, updated_at = NOW()
WHERE id = sqlc.arg(subscription_id)::uuid;

-- name: GetClusterSubscriptionRecord :one
SELECT a.id, a.tenant_id, a.cluster_id, a.access_level, a.subscription_status,
       a.resource_limits, a.requested_at, a.approved_at, a.approved_by,
       a.rejection_reason, a.expires_at, a.created_at, a.updated_at,
       c.cluster_name, t.name AS tenant_name
FROM quartermaster.tenant_cluster_access a
JOIN quartermaster.infrastructure_clusters c ON a.cluster_id = c.cluster_id
JOIN quartermaster.tenants t ON a.tenant_id = t.id
WHERE a.tenant_id = sqlc.arg(tenant_id)::uuid AND a.cluster_id = sqlc.arg(cluster_id);

-- name: ListPeerClusters :many
WITH my_tenants AS (
    SELECT DISTINCT mine.tenant_id
    FROM quartermaster.tenant_cluster_access mine
    WHERE mine.cluster_id = sqlc.arg(requesting_cluster_id)
      AND mine.is_active = TRUE AND mine.subscription_status = 'active'
      AND mine.access_source <> 'unknown'
      AND (mine.expires_at IS NULL OR mine.expires_at > NOW())
), peer_clusters AS (
    SELECT tca.cluster_id, array_agg(DISTINCT tca.tenant_id::text)::text[] AS shared_tenant_ids
    FROM quartermaster.tenant_cluster_access tca
    JOIN my_tenants mt ON tca.tenant_id = mt.tenant_id
    WHERE tca.cluster_id != sqlc.arg(requesting_cluster_id)
      AND tca.is_active = TRUE AND tca.subscription_status = 'active'
      AND tca.access_source <> 'unknown'
      AND (tca.expires_at IS NULL OR tca.expires_at > NOW())
    GROUP BY tca.cluster_id
)
SELECT pc.cluster_id, pc.shared_tenant_ids, ic.cluster_name, ic.cluster_type,
       COALESCE((
           SELECT si.advertise_host || ':' || si.port
           FROM quartermaster.service_cluster_assignments sca
           JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
           JOIN quartermaster.services svc ON svc.service_id = si.service_id
           WHERE sca.cluster_id = pc.cluster_id AND sca.is_active = TRUE
             AND svc.type = 'foghorn' AND si.status = 'running'
             AND si.health_status = 'healthy' AND si.protocol = 'grpc'
           -- Any healthy replica serves the cluster; the choice must be stable, because a
           -- peer leader re-dials whenever the address changes. Heartbeats move updated_at,
           -- so ordering by it alternated between replicas and tore down live channels.
           ORDER BY si.advertise_host ASC, si.port ASC, si.id ASC LIMIT 1
       ), '')::text AS foghorn_addr,
       -- Every healthy replica in the same stable order, so request/response RPCs
       -- can fail over while the peer channel keeps using the first address.
       ARRAY(
           SELECT DISTINCT ON (si.advertise_host, si.port) si.advertise_host || ':' || si.port
           FROM quartermaster.service_cluster_assignments sca
           JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
           JOIN quartermaster.services svc ON svc.service_id = si.service_id
           WHERE sca.cluster_id = pc.cluster_id AND sca.is_active = TRUE
             AND svc.type = 'foghorn' AND si.status = 'running'
             AND si.health_status = 'healthy' AND si.protocol = 'grpc'
           ORDER BY si.advertise_host ASC, si.port ASC, si.id ASC
       )::text[] AS foghorn_addrs,
       COALESCE(NULLIF(ic.control_cell_id, ''), NULLIF(ic.cell_id, ''), ic.cluster_id)::text AS control_cell_id
FROM peer_clusters pc
JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = pc.cluster_id
WHERE ic.is_active = TRUE;

-- name: PeerCensusFingerprint :one
-- Digests exactly the values ListPeerClusters derives a peer set from: active
-- tenant reach per cluster, active cluster identity and control cell, and the
-- address of every assigned healthy gRPC Foghorn. Heartbeats and health
-- refreshes that rewrite a row without changing any of these leave the digest
-- unchanged, so they do not wake peer subscribers. Expiry is evaluated against
-- NOW(), so a grant lapsing on its own also changes the digest.
SELECT md5(format('%L|%L|%L',
    (SELECT string_agg(format('%L/%L', tca.tenant_id, tca.cluster_id), ',' ORDER BY tca.tenant_id, tca.cluster_id)
     FROM quartermaster.tenant_cluster_access tca
     WHERE tca.is_active = TRUE AND tca.subscription_status = 'active'
       AND tca.access_source <> 'unknown'
       AND (tca.expires_at IS NULL OR tca.expires_at > NOW())),
    (SELECT string_agg(format('%L/%L/%L/%L/%L', ic.cluster_id, ic.cluster_name, ic.cluster_type, ic.control_cell_id, ic.cell_id), ',' ORDER BY ic.cluster_id)
     FROM quartermaster.infrastructure_clusters ic
     WHERE ic.is_active = TRUE),
    (SELECT string_agg(format('%L/%L/%L', sca.cluster_id, si.advertise_host, si.port), ',' ORDER BY sca.cluster_id, si.advertise_host, si.port, si.id)
     FROM quartermaster.services svc
     JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
     JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id
     WHERE svc.type = 'foghorn' AND sca.is_active = TRUE
       AND si.status = 'running' AND si.health_status = 'healthy' AND si.protocol = 'grpc')
))::text AS fingerprint;
