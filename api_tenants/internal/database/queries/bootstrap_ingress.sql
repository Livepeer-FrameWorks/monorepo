-- name: GetBootstrapTLSBundle :one
SELECT cluster_id,
       COALESCE(domains::text, '[]')::text AS domains,
       issuer,
       email
FROM quartermaster.tls_bundles
WHERE bundle_id = sqlc.arg(bundle_id)::text;

-- name: InsertBootstrapTLSBundle :exec
INSERT INTO quartermaster.tls_bundles (
    bundle_id, cluster_id, domains, issuer, email, updated_at
) VALUES (
    sqlc.arg(bundle_id)::text,
    sqlc.arg(cluster_id)::text,
    sqlc.arg(domains)::text::jsonb,
    sqlc.arg(issuer)::text,
    sqlc.arg(email)::text,
    NOW()
);

-- name: UpdateBootstrapTLSBundle :exec
WITH input AS (
    SELECT sqlc.arg(bundle_id)::text AS bundle_id,
           sqlc.arg(domains)::text AS domains,
           sqlc.arg(issuer)::text AS issuer,
           sqlc.arg(email)::text AS email
)
UPDATE quartermaster.tls_bundles
SET domains = input.domains::jsonb,
    issuer = input.issuer,
    email = input.email,
    updated_at = NOW()
FROM input
WHERE tls_bundles.bundle_id = input.bundle_id;

-- name: GetBootstrapIngressSite :one
SELECT cluster_id,
       node_id,
       COALESCE(domains::text, '[]')::text AS domains,
       tls_bundle_id,
       kind,
       upstream
FROM quartermaster.ingress_sites
WHERE site_id = sqlc.arg(site_id)::text;

-- name: InsertBootstrapIngressSite :exec
INSERT INTO quartermaster.ingress_sites (
    site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, updated_at
) VALUES (
    sqlc.arg(site_id)::text,
    sqlc.arg(cluster_id)::text,
    sqlc.arg(node_id)::text,
    sqlc.arg(domains)::text::jsonb,
    sqlc.arg(tls_bundle_id)::text,
    sqlc.arg(kind)::text,
    sqlc.arg(upstream)::text,
    NOW()
);

-- name: UpdateBootstrapIngressSite :exec
WITH input AS (
    SELECT sqlc.arg(site_id)::text AS site_id,
           sqlc.arg(domains)::text AS domains,
           sqlc.arg(tls_bundle_id)::text AS tls_bundle_id,
           sqlc.arg(kind)::text AS kind,
           sqlc.arg(upstream)::text AS upstream
)
UPDATE quartermaster.ingress_sites
SET domains = input.domains::jsonb,
    tls_bundle_id = input.tls_bundle_id,
    kind = input.kind,
    upstream = input.upstream,
    updated_at = NOW()
FROM input
WHERE ingress_sites.site_id = input.site_id;
