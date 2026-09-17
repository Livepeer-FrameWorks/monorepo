-- name: GetClusterScope :one
-- The cluster-to-owner map is keyed by cluster and has no caller tenant: it is
-- what decides an alert's tenant, so these queries cannot filter by one.
SELECT scope, tenant_id, verified, revision, applied_revision
FROM lookout.cluster_scopes
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: EnsureClusterScope :exec
-- Creates an unverified platform row for a cluster Quartermaster could not
-- answer for, so ingestion always has a row to lock. An existing row is kept.
INSERT INTO lookout.cluster_scopes (cluster_id, scope, verified)
VALUES (sqlc.arg(cluster_id), 'platform', false)
ON CONFLICT (cluster_id) DO NOTHING;

-- name: LockClusterScopeShared :one
-- Ingestion holds this lock until its transaction commits, so storing a new
-- owner for the cluster waits for the incident it writes.
SELECT scope, tenant_id, verified
FROM lookout.cluster_scopes
WHERE cluster_id = sqlc.arg(cluster_id)
FOR SHARE;

-- name: StoreVerifiedClusterScope :exec
-- Records Quartermaster's answer unless the stored answer is verified and not
-- older. A NotFound answer (NULL source_updated_at) only replaces an unverified
-- row: clusters are not deleted, so it never supersedes a known owner.
INSERT INTO lookout.cluster_scopes (cluster_id, scope, tenant_id, verified, source_updated_at, revision, updated_at)
VALUES (
    sqlc.arg(cluster_id),
    sqlc.arg(scope),
    sqlc.narg(tenant_id)::uuid,
    true,
    sqlc.narg(source_updated_at)::timestamptz,
    1,
    NOW()
)
ON CONFLICT (cluster_id) DO UPDATE
SET scope = EXCLUDED.scope,
    tenant_id = EXCLUDED.tenant_id,
    verified = true,
    source_updated_at = EXCLUDED.source_updated_at,
    revision = lookout.cluster_scopes.revision + 1,
    updated_at = NOW()
WHERE NOT lookout.cluster_scopes.verified
   OR (EXCLUDED.source_updated_at IS NOT NULL
       AND (lookout.cluster_scopes.source_updated_at IS NULL
            OR EXCLUDED.source_updated_at >= lookout.cluster_scopes.source_updated_at));

-- name: LockClusterScopeForUpdate :one
-- Reconciliation holds this lock while it moves the cluster's open incidents.
SELECT scope, tenant_id, verified, revision
FROM lookout.cluster_scopes
WHERE cluster_id = sqlc.arg(cluster_id)
FOR UPDATE;

-- name: MarkClusterScopeApplied :exec
-- Records that the open incidents were moved to the scope of this revision.
UPDATE lookout.cluster_scopes
SET applied_revision = GREATEST(applied_revision, sqlc.arg(revision)::bigint)
WHERE cluster_id = sqlc.arg(cluster_id);
