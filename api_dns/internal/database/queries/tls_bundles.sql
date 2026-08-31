-- name: GetTLSBundle :one
SELECT bundle.id, bundle.bundle_id, bundle.domains, bundle.cert_pem, bundle.key_pem,
       bundle.expires_at, bundle.created_at, bundle.updated_at, bundle.issuer_ca, bundle.version
FROM navigator.tls_bundles AS bundle
WHERE bundle.bundle_id = $1
  AND (
      bundle.bundle_id NOT LIKE 'tenant:%'
      OR EXISTS (
          SELECT 1
          FROM navigator.tenant_aliases AS alias
          WHERE bundle.bundle_id = 'tenant:' || alias.tenant_id::text
      )
  );

-- name: GetTLSBundleForIssuance :one
SELECT bundle.id, bundle.bundle_id, bundle.domains, bundle.cert_pem, bundle.key_pem,
       bundle.expires_at, bundle.created_at, bundle.updated_at, bundle.issuer_ca, bundle.version
FROM navigator.tls_bundles AS bundle
WHERE bundle.bundle_id = $1
  AND (
      bundle.bundle_id NOT LIKE 'tenant:%'
      OR EXISTS (
          SELECT 1
          FROM navigator.tenant_aliases AS alias
          WHERE bundle.bundle_id = 'tenant:' || alias.tenant_id::text
      )
  );

-- name: SaveTLSBundle :one
WITH tenant_authority AS MATERIALIZED (
    SELECT alias.tenant_id
    FROM navigator.tenant_aliases AS alias
    WHERE sqlc.arg(bundle_id)::text = 'tenant:' || alias.tenant_id::text
      AND alias.status <> 'tearing_down'
      AND alias.subdomain = sqlc.arg(expected_subdomain)::text
      AND alias.authority_version = sqlc.arg(expected_authority_version)::bigint
    FOR UPDATE OF alias
), bundle_write AS (
    SELECT
        sqlc.arg(bundle_id)::text AS bundle_id,
        sqlc.arg(domains)::jsonb AS domains,
        sqlc.arg(cert_pem)::text AS cert_pem,
        sqlc.arg(key_pem)::text AS key_pem,
        sqlc.arg(expires_at)::timestamptz AS expires_at,
        sqlc.arg(issuer_ca)::text AS issuer_ca,
        sqlc.arg(version)::text AS version
    WHERE sqlc.arg(bundle_id)::text NOT LIKE 'tenant:%'
       OR EXISTS (SELECT 1 FROM tenant_authority)
), persisted_bundle AS (
    INSERT INTO navigator.tls_bundles (
        bundle_id, domains, cert_pem, key_pem, expires_at, issuer_ca, version, updated_at
    )
    SELECT bundle_id, domains, cert_pem, key_pem, expires_at, issuer_ca, version, NOW()
    FROM bundle_write
    ON CONFLICT (bundle_id) DO UPDATE SET
        domains = EXCLUDED.domains,
        cert_pem = EXCLUDED.cert_pem,
        key_pem = EXCLUDED.key_pem,
        expires_at = EXCLUDED.expires_at,
        issuer_ca = EXCLUDED.issuer_ca,
        version = EXCLUDED.version,
        updated_at = NOW()
    RETURNING id, created_at, bundle_id, domains, expires_at, issuer_ca
), issued_alias AS (
    UPDATE navigator.tenant_aliases AS alias
    SET status = 'cert_issued',
        cert_issued_at = COALESCE(alias.cert_issued_at, NOW()),
        last_error = NULL,
        updated_at = NOW()
    FROM tenant_authority, persisted_bundle
    WHERE persisted_bundle.bundle_id = 'tenant:' || tenant_authority.tenant_id::text
      AND alias.tenant_id = tenant_authority.tenant_id
    RETURNING alias.tenant_id
), updated_custom_domains AS (
    UPDATE navigator.tenant_custom_domains AS custom_domain
    SET issuer_id = NULLIF(persisted_bundle.issuer_ca, ''),
        cert_expires_at = persisted_bundle.expires_at,
        updated_at = NOW()
    FROM tenant_authority, persisted_bundle
    WHERE custom_domain.tenant_id = tenant_authority.tenant_id
      AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
      AND persisted_bundle.domains ? custom_domain.domain
    RETURNING custom_domain.tenant_id
)
SELECT persisted_bundle.id, persisted_bundle.created_at
FROM persisted_bundle
WHERE (
       persisted_bundle.bundle_id NOT LIKE 'tenant:%'
       OR EXISTS (SELECT 1 FROM issued_alias)
  )
  AND (SELECT COUNT(*) FROM updated_custom_domains) >= 0;

-- name: ListExpiringTLSBundles :many
SELECT id, bundle_id, domains, cert_pem, key_pem, expires_at, created_at, updated_at, issuer_ca, version
FROM navigator.tls_bundles
WHERE expires_at < $1
ORDER BY expires_at ASC;
