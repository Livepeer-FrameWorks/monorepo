-- name: EnsureTenantAlias :one
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(subdomain), 'cert_issuing', NOW(), NOW())
ON CONFLICT (tenant_id) DO UPDATE SET
    subdomain = EXCLUDED.subdomain,
    status = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                  THEN 'cert_issuing' ELSE navigator.tenant_aliases.status END,
    cert_issued_at = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                          THEN NULL ELSE navigator.tenant_aliases.cert_issued_at END,
    last_error = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                      THEN NULL ELSE navigator.tenant_aliases.last_error END,
    updated_at = NOW()
RETURNING tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at;

-- name: GetTenantAlias :one
SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantAliasesByStatus :many
SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE status = ANY(sqlc.arg(statuses)::text[])
ORDER BY updated_at ASC;

-- name: ListPendingTenantAliases :many
SELECT tenant_id, subdomain, status, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE status IN ('cert_issuing', 'cert_failed')
ORDER BY updated_at ASC;

-- name: SetTenantAliasStatus :exec
UPDATE navigator.tenant_aliases
SET status = sqlc.arg(status),
    cert_issued_at = CASE WHEN sqlc.arg(status) = 'cert_issued' THEN NOW() ELSE cert_issued_at END,
    last_error = NULLIF(sqlc.arg(err_msg)::text, ''),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: DeleteTenantAlias :exec
DELETE FROM navigator.tenant_aliases WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: UpsertTenantEdgeApplyState :exec
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id,
    state, last_seed_version, last_ack_at, in_dns_at, updated_at
)
VALUES (
    sqlc.arg(tenant_id)::uuid, sqlc.arg(cluster_id), sqlc.arg(node_id), sqlc.arg(bundle_id),
    sqlc.arg(state), sqlc.narg(last_seed_version), sqlc.narg(last_ack_at), sqlc.narg(in_dns_at), NOW()
)
ON CONFLICT (tenant_id, node_id, bundle_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id,
    state = EXCLUDED.state,
    last_seed_version = EXCLUDED.last_seed_version,
    last_ack_at = EXCLUDED.last_ack_at,
    in_dns_at = EXCLUDED.in_dns_at,
    updated_at = NOW();

-- name: TenantAliasHasDNS :one
SELECT EXISTS (
    SELECT 1 FROM navigator.tenant_edge_apply_state
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND state = 'in_dns'
);

-- name: ListTenantEdgeApplyState :many
SELECT tenant_id, cluster_id, node_id, bundle_id,
       state, last_seed_version, last_ack_at, in_dns_at, updated_at
FROM navigator.tenant_edge_apply_state
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY updated_at DESC;

-- name: ListTenantEdgeApplyStateByState :many
SELECT tenant_id, cluster_id, node_id, bundle_id,
       state, last_seed_version, last_ack_at, in_dns_at, updated_at
FROM navigator.tenant_edge_apply_state
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND state = sqlc.arg(state)
ORDER BY updated_at DESC;

-- name: DeleteTenantEdgeApplyState :exec
DELETE FROM navigator.tenant_edge_apply_state WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: DeleteTenantEdgeApplyStateForCluster :exec
DELETE FROM navigator.tenant_edge_apply_state
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND cluster_id = sqlc.arg(cluster_id);

-- name: InsertTenantAliasRetirement :exec
INSERT INTO navigator.tenant_alias_retirements (tenant_id, subdomain)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(subdomain))
ON CONFLICT (tenant_id, subdomain) DO NOTHING;

-- name: ListTenantAliasRetirements :many
SELECT tenant_id, subdomain, requested_at, attempts, last_error
FROM navigator.tenant_alias_retirements
ORDER BY requested_at ASC;

-- name: ListTenantAliasRetirementLabels :many
SELECT subdomain
FROM navigator.tenant_alias_retirements
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY requested_at ASC;

-- name: DeleteTenantAliasRetirement :exec
DELETE FROM navigator.tenant_alias_retirements
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND subdomain = sqlc.arg(subdomain);

-- name: RecordTenantAliasRetirementFailure :exec
UPDATE navigator.tenant_alias_retirements
SET attempts = attempts + 1, last_error = NULLIF(sqlc.arg(err_msg)::text, '')
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND subdomain = sqlc.arg(subdomain);
