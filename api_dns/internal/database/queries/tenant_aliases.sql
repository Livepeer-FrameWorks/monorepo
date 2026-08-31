-- name: EnsureTenantAlias :one
WITH previous_authority AS MATERIALIZED (
    SELECT tenant_id, subdomain, status, authority_version
    FROM navigator.tenant_aliases
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
    FOR UPDATE
), ensured_alias AS (
    INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status, created_at, updated_at)
    VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(subdomain), 'cert_issuing', NOW(), NOW())
    ON CONFLICT (tenant_id) DO UPDATE SET
        subdomain = EXCLUDED.subdomain,
        status = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                           OR navigator.tenant_aliases.status = 'tearing_down'
                      THEN 'cert_issuing' ELSE navigator.tenant_aliases.status END,
        authority_version = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                                      OR navigator.tenant_aliases.status = 'tearing_down'
                                 THEN navigator.tenant_aliases.authority_version + 1
                                 ELSE navigator.tenant_aliases.authority_version END,
        cert_issued_at = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                                   OR navigator.tenant_aliases.status = 'tearing_down'
                              THEN NULL ELSE navigator.tenant_aliases.cert_issued_at END,
        last_error = CASE WHEN navigator.tenant_aliases.subdomain IS DISTINCT FROM EXCLUDED.subdomain
                               OR navigator.tenant_aliases.status = 'tearing_down'
                          THEN NULL ELSE navigator.tenant_aliases.last_error END,
        updated_at = NOW()
    RETURNING tenant_id, subdomain, status, authority_version, cert_issued_at, last_error, created_at, updated_at
), reset_edge_authority AS (
    UPDATE navigator.tenant_edge_apply_state AS edge
    SET state = 'pending_distribute',
        in_dns_at = NULL,
        updated_at = NOW()
    FROM previous_authority
    WHERE edge.tenant_id = previous_authority.tenant_id
      AND (
          previous_authority.subdomain IS DISTINCT FROM sqlc.arg(subdomain)
          OR previous_authority.status = 'tearing_down'
      )
    RETURNING edge.tenant_id
)
SELECT tenant_id, subdomain, status, authority_version, cert_issued_at, last_error, created_at, updated_at
FROM ensured_alias
WHERE (SELECT COUNT(*) FROM reset_edge_authority) >= 0;

-- name: GetTenantAlias :one
SELECT tenant_id, subdomain, status, authority_version, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ListTenantAliasesByStatus :many
SELECT tenant_id, subdomain, status, authority_version, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE status = ANY(sqlc.arg(statuses)::text[])
ORDER BY updated_at ASC;

-- name: ListPendingTenantAliases :many
SELECT tenant_id, subdomain, status, authority_version, cert_issued_at, last_error, created_at, updated_at
FROM navigator.tenant_aliases
WHERE status IN ('cert_issuing', 'cert_failed')
ORDER BY updated_at ASC;

-- name: SetTenantAliasStatus :execrows
UPDATE navigator.tenant_aliases
SET status = sqlc.arg(status),
    authority_version = CASE WHEN sqlc.arg(status) = 'tearing_down' AND status <> 'tearing_down'
                             THEN authority_version + 1 ELSE authority_version END,
    cert_issued_at = CASE WHEN sqlc.arg(status) = 'cert_issued' THEN COALESCE(cert_issued_at, NOW()) ELSE cert_issued_at END,
    last_error = NULLIF(sqlc.arg(err_msg)::text, ''),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND (
      sqlc.arg(status) = 'tearing_down'
      OR (
          (
              (sqlc.arg(status) = 'cert_issued' AND status IN ('cert_issuing', 'cert_issued', 'cert_failed'))
              OR (sqlc.arg(status) = 'cert_failed' AND status IN ('cert_issuing', 'cert_failed'))
          )
          AND subdomain = sqlc.arg(expected_subdomain)
          AND authority_version = sqlc.arg(expected_authority_version)::bigint
      )
  );

-- name: DeleteTenantAlias :execrows
WITH teardown_authority AS MATERIALIZED (
    SELECT tenant_id
    FROM navigator.tenant_aliases
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
      AND status = 'tearing_down'
    FOR UPDATE
), deleted_alias_certificates AS (
    DELETE FROM navigator.certificates AS certificate
    USING teardown_authority
    WHERE certificate.tenant_id = teardown_authority.tenant_id
      AND NOT EXISTS (
          SELECT 1
          FROM navigator.tenant_custom_domains AS custom_domain
          WHERE custom_domain.tenant_id = certificate.tenant_id
            AND custom_domain.domain = certificate.domain
            AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
      )
), deleted_alias_bundle AS (
    DELETE FROM navigator.tls_bundles
    USING teardown_authority
    WHERE bundle_id = 'tenant:' || teardown_authority.tenant_id::text
), deleted_unused_acme_accounts AS (
    DELETE FROM navigator.acme_accounts AS account
    USING teardown_authority
    WHERE account.tenant_id = teardown_authority.tenant_id
      AND NOT EXISTS (
          SELECT 1
          FROM navigator.tenant_custom_domains AS custom_domain
          WHERE custom_domain.tenant_id = teardown_authority.tenant_id
            AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
      )
)
DELETE FROM navigator.tenant_aliases AS alias
USING teardown_authority
WHERE alias.tenant_id = teardown_authority.tenant_id
  AND alias.status = 'tearing_down';

-- name: MarkTenantEdgeInDNS :execrows
UPDATE navigator.tenant_edge_apply_state AS edge
SET state = 'in_dns',
    in_dns_at = COALESCE(in_dns_at, NOW()),
    updated_at = NOW()
WHERE edge.tenant_id = sqlc.arg(tenant_id)::uuid
  AND edge.node_id = sqlc.arg(node_id)
  AND edge.bundle_id = sqlc.arg(bundle_id)
  AND edge.state = 'applied'
  AND edge.bundle_version <> ''
  AND edge.bundle_version = sqlc.arg(snapshot_bundle_version)
  AND EXISTS (
      SELECT 1
      FROM navigator.tls_bundles AS bundle
      WHERE bundle.bundle_id = edge.bundle_id
        AND bundle.version = edge.bundle_version
  )
  -- Promotion enforces active cluster authority itself. The publisher's list
  -- already filters on it, but a residual row beneath a tombstone must not be
  -- promotable by any caller-held guarantee alone.
  AND EXISTS (
      SELECT 1
      FROM navigator.tenant_alias_cluster_authority AS cluster_authority
      WHERE cluster_authority.tenant_id = edge.tenant_id
        AND cluster_authority.cluster_id = edge.cluster_id
        AND cluster_authority.state = 'active'
  )
  AND edge.last_seed_version IS NOT DISTINCT FROM sqlc.narg(snapshot_seed_version)
  AND edge.last_delivery_sequence = sqlc.arg(snapshot_delivery_sequence);

-- name: MarkTenantEdgeNotInDNS :execrows
UPDATE navigator.tenant_edge_apply_state
SET state = 'applied',
    in_dns_at = NULL,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND node_id = sqlc.arg(node_id)
  AND bundle_id = sqlc.arg(bundle_id)
  AND state = 'in_dns'
  AND bundle_version = sqlc.arg(snapshot_bundle_version)
  AND last_seed_version IS NOT DISTINCT FROM sqlc.narg(snapshot_seed_version)
  AND last_delivery_sequence = sqlc.arg(snapshot_delivery_sequence);

-- name: UpsertTenantEdgeApplyAck :one
WITH authority AS MATERIALIZED (
    SELECT alias.tenant_id, alias.status
    FROM navigator.tenant_aliases AS alias
    WHERE alias.tenant_id = sqlc.arg(tenant_id)::uuid
    FOR KEY SHARE OF alias
), parent AS MATERIALIZED (
    SELECT authority.tenant_id
    FROM authority
    JOIN navigator.tenant_alias_cluster_authority AS cluster_authority
      ON cluster_authority.tenant_id = authority.tenant_id
     AND cluster_authority.cluster_id = sqlc.arg(cluster_id)
     AND cluster_authority.state = 'active'
    JOIN navigator.tls_bundles AS bundle
      ON bundle.bundle_id = sqlc.arg(bundle_id)
     AND bundle.bundle_id = 'tenant:' || authority.tenant_id::text
     AND (
         sqlc.arg(bundle_version)::text = ''
         OR bundle.version = sqlc.arg(bundle_version)
    )
    WHERE authority.status = 'cert_issued'
    -- state = 'active' must remain a qual on this locked relation: the blocked
    -- re-check after a conflicting revocation commits (EvalPlanQual) only
    -- re-evaluates predicates on the locked row itself, so moving the state
    -- test into a subquery would silently stop fencing.
    FOR SHARE OF cluster_authority
), cluster_pair AS MATERIALIZED (
    -- Locks the pair row WITHOUT a state qual so classification sees its
    -- post-commit state: an ACK racing a revocation blocks here, and the
    -- EvalPlanQual re-check returns the latest tuple ('revoked'), which the
    -- statement's original snapshot (taken before the revocation committed)
    -- would never show a plain EXISTS subquery.
    SELECT cluster_authority.state
    FROM authority
    JOIN navigator.tenant_alias_cluster_authority AS cluster_authority
      ON cluster_authority.tenant_id = authority.tenant_id
     AND cluster_authority.cluster_id = sqlc.arg(cluster_id)
    FOR SHARE OF cluster_authority
), upserted AS (
    INSERT INTO navigator.tenant_edge_apply_state (
        tenant_id, cluster_id, node_id, bundle_id, bundle_version,
        state, last_seed_version, last_delivery_sequence, last_ack_at, in_dns_at, updated_at
    )
    SELECT
        parent.tenant_id, sqlc.arg(cluster_id), sqlc.arg(node_id), sqlc.arg(bundle_id), sqlc.arg(bundle_version),
        sqlc.arg(state), sqlc.arg(last_seed_version), sqlc.arg(last_delivery_sequence), sqlc.arg(last_ack_at), NULL, NOW()
    FROM parent
    ON CONFLICT (tenant_id, node_id, bundle_id) DO UPDATE SET
        cluster_id = EXCLUDED.cluster_id,
        bundle_version = EXCLUDED.bundle_version,
        state = CASE
            WHEN EXCLUDED.state = 'applied'
             AND navigator.tenant_edge_apply_state.state = 'in_dns'
             AND (
                 (
                     navigator.tenant_edge_apply_state.last_seed_version = EXCLUDED.last_seed_version
                     AND
                     EXCLUDED.bundle_version <> ''
                     AND navigator.tenant_edge_apply_state.bundle_version = EXCLUDED.bundle_version
                 )
                 OR (
                     EXCLUDED.bundle_version = ''
                     AND navigator.tenant_edge_apply_state.bundle_version = ''
                 )
             )
            THEN navigator.tenant_edge_apply_state.state
            ELSE EXCLUDED.state
        END,
        last_seed_version = EXCLUDED.last_seed_version,
        last_delivery_sequence = CASE
            WHEN EXCLUDED.last_delivery_sequence = 0
            THEN navigator.tenant_edge_apply_state.last_delivery_sequence
            ELSE EXCLUDED.last_delivery_sequence
        END,
        last_ack_at = EXCLUDED.last_ack_at,
        in_dns_at = CASE
            WHEN EXCLUDED.state = 'applied'
             AND navigator.tenant_edge_apply_state.state = 'in_dns'
             AND (
                 (
                     navigator.tenant_edge_apply_state.last_seed_version = EXCLUDED.last_seed_version
                     AND
                     EXCLUDED.bundle_version <> ''
                     AND navigator.tenant_edge_apply_state.bundle_version = EXCLUDED.bundle_version
                 )
                 OR (
                     EXCLUDED.bundle_version = ''
                     AND navigator.tenant_edge_apply_state.bundle_version = ''
                 )
             )
            THEN navigator.tenant_edge_apply_state.in_dns_at
            ELSE NULL
        END,
        updated_at = NOW()
    WHERE navigator.tenant_edge_apply_state.last_seed_version IS NULL
       OR navigator.tenant_edge_apply_state.last_seed_version < EXCLUDED.last_seed_version
       OR (
           navigator.tenant_edge_apply_state.last_seed_version = EXCLUDED.last_seed_version
           AND (
               (
                   EXCLUDED.last_delivery_sequence > 0
                   AND EXCLUDED.last_delivery_sequence > navigator.tenant_edge_apply_state.last_delivery_sequence
               )
               OR (
                   EXCLUDED.last_delivery_sequence = 0
                   AND navigator.tenant_edge_apply_state.last_delivery_sequence = 0
                   AND (
                       (EXCLUDED.state = 'pending_apply' AND navigator.tenant_edge_apply_state.state <> 'pending_apply')
                       OR (EXCLUDED.state = 'applied' AND navigator.tenant_edge_apply_state.state = 'pending_apply')
                   )
               )
           )
       )
    RETURNING 1
)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM upserted) THEN 'accepted'
    WHEN EXISTS (SELECT 1 FROM authority)
     AND EXISTS (
         SELECT 1
         FROM cluster_pair
         WHERE cluster_pair.state = 'revoked'
     ) THEN 'revoked'
    WHEN EXISTS (SELECT 1 FROM authority) THEN 'stale'
    ELSE 'missing_parent'
END::text AS outcome;

-- name: TenantAliasHasDNS :one
SELECT EXISTS (
    SELECT 1
    FROM navigator.tenant_edge_apply_state AS edge
    JOIN navigator.tenant_aliases AS alias ON alias.tenant_id = edge.tenant_id
    JOIN navigator.tenant_alias_cluster_authority AS cluster_authority
      ON cluster_authority.tenant_id = edge.tenant_id
     AND cluster_authority.cluster_id = edge.cluster_id
     AND cluster_authority.state = 'active'
    JOIN navigator.tls_bundles AS bundle ON bundle.bundle_id = edge.bundle_id
    WHERE edge.tenant_id = sqlc.arg(tenant_id)::uuid
      AND edge.bundle_id = 'tenant:' || edge.tenant_id::text
      AND edge.state = 'in_dns'
      AND alias.status = 'cert_issued'
      AND edge.bundle_version <> ''
      AND edge.bundle_version = bundle.version
);

-- name: TenantAliasClusterAuthorityState :one
SELECT COALESCE((
    SELECT state
    FROM navigator.tenant_alias_cluster_authority
    WHERE tenant_id = sqlc.arg(tenant_id)::uuid
      AND cluster_id = sqlc.arg(cluster_id)
), '')::text;

-- name: ListTenantAliasAuthorizedClusters :many
SELECT cluster_id
FROM navigator.tenant_alias_cluster_authority
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND state = 'active'
ORDER BY cluster_id;

-- name: GrantTenantAliasClusterAuthority :one
INSERT INTO navigator.tenant_alias_cluster_authority (
    tenant_id, cluster_id, state, authority_sequence, updated_at
)
SELECT alias.tenant_id, sqlc.arg(cluster_id), 'active', sqlc.arg(authority_sequence), NOW()
FROM navigator.tenant_aliases AS alias
WHERE alias.tenant_id = sqlc.arg(tenant_id)::uuid
  AND alias.status <> 'tearing_down'
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    state = 'active',
    authority_sequence = EXCLUDED.authority_sequence,
    updated_at = NOW()
WHERE EXCLUDED.authority_sequence > navigator.tenant_alias_cluster_authority.authority_sequence
   OR (
       EXCLUDED.authority_sequence = navigator.tenant_alias_cluster_authority.authority_sequence
       AND navigator.tenant_alias_cluster_authority.state = 'active'
   )
RETURNING true;

-- name: RevokeTenantAliasClusterAuthority :one
-- Tombstone only. The edge delete runs as its own later statement
-- (DeleteTenantEdgeApplyStateForRevokedCluster) so its snapshot postdates this
-- statement's row lock: an ACK that committed while this tombstone waited is
-- then visible to the delete. Making the tombstone the first statement also
-- creates the serialization row for a previously unseen tenant/cluster pair of
-- an existing alias; with no alias row the FK-backed SELECT finds no parent.
-- Zero rows therefore means an ordered newer decision already superseded this
-- revocation, or the alias intent is absent — the caller reports applied=false
-- for both and the Quartermaster backstop re-drives the absent-alias case.
INSERT INTO navigator.tenant_alias_cluster_authority (
    tenant_id, cluster_id, state, authority_sequence, updated_at
)
SELECT alias.tenant_id, sqlc.arg(cluster_id), 'revoked', sqlc.arg(authority_sequence), NOW()
FROM navigator.tenant_aliases AS alias
WHERE alias.tenant_id = sqlc.arg(tenant_id)::uuid
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    state = 'revoked',
    authority_sequence = EXCLUDED.authority_sequence,
    updated_at = NOW()
WHERE EXCLUDED.authority_sequence >= navigator.tenant_alias_cluster_authority.authority_sequence
RETURNING true;

-- name: DeleteTenantEdgeApplyStateForRevokedCluster :execrows
-- Fenced on the tombstone so a replayed delete cannot outrun a newer grant.
DELETE FROM navigator.tenant_edge_apply_state AS edge
USING navigator.tenant_alias_cluster_authority AS cluster_authority
WHERE cluster_authority.tenant_id = sqlc.arg(tenant_id)::uuid
  AND cluster_authority.cluster_id = sqlc.arg(cluster_id)
  AND cluster_authority.state = 'revoked'
  AND edge.tenant_id = cluster_authority.tenant_id
  AND edge.cluster_id = cluster_authority.cluster_id;

-- name: ListTenantEdgeApplyState :many
SELECT edge.tenant_id, edge.cluster_id, edge.node_id, edge.bundle_id, edge.bundle_version,
       edge.state, edge.last_seed_version, edge.last_delivery_sequence, edge.last_ack_at, edge.in_dns_at, edge.updated_at,
       COALESCE(bundle.version, '')::text AS current_bundle_version,
       (bundle.bundle_id IS NOT NULL)::boolean AS current_bundle_present
FROM navigator.tenant_edge_apply_state AS edge
JOIN navigator.tenant_alias_cluster_authority AS cluster_authority
  ON cluster_authority.tenant_id = edge.tenant_id
 AND cluster_authority.cluster_id = edge.cluster_id
 AND cluster_authority.state = 'active'
LEFT JOIN navigator.tls_bundles AS bundle ON bundle.bundle_id = edge.bundle_id
WHERE edge.tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY edge.updated_at DESC;

-- name: ListTenantEdgeApplyStateByState :many
SELECT edge.tenant_id, edge.cluster_id, edge.node_id, edge.bundle_id, edge.bundle_version,
       edge.state, edge.last_seed_version, edge.last_delivery_sequence, edge.last_ack_at, edge.in_dns_at, edge.updated_at,
       COALESCE(bundle.version, '')::text AS current_bundle_version,
       (bundle.bundle_id IS NOT NULL)::boolean AS current_bundle_present
FROM navigator.tenant_edge_apply_state AS edge
JOIN navigator.tenant_alias_cluster_authority AS cluster_authority
  ON cluster_authority.tenant_id = edge.tenant_id
 AND cluster_authority.cluster_id = edge.cluster_id
 AND cluster_authority.state = 'active'
LEFT JOIN navigator.tls_bundles AS bundle ON bundle.bundle_id = edge.bundle_id
WHERE edge.tenant_id = sqlc.arg(tenant_id)::uuid
  AND edge.state = sqlc.arg(state)
ORDER BY edge.updated_at DESC;

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
