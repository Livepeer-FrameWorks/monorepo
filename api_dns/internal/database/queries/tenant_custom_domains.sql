-- name: EnsureTenantCustomDomain :one
INSERT INTO navigator.tenant_custom_domains
    (tenant_id, domain, status, acme_dns_subdomain, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(domain), 'pending_verification', sqlc.arg(acme_dns_subdomain), NOW(), NOW())
ON CONFLICT (tenant_id, domain) DO UPDATE SET
    status = CASE WHEN navigator.tenant_custom_domains.status = 'tearing_down'
                  THEN 'pending_verification' ELSE navigator.tenant_custom_domains.status END,
    issuer_id = CASE WHEN navigator.tenant_custom_domains.status = 'tearing_down'
                     THEN NULL ELSE navigator.tenant_custom_domains.issuer_id END,
    cert_issued_at = CASE WHEN navigator.tenant_custom_domains.status = 'tearing_down'
                          THEN NULL ELSE navigator.tenant_custom_domains.cert_issued_at END,
    cert_expires_at = CASE WHEN navigator.tenant_custom_domains.status = 'tearing_down'
                           THEN NULL ELSE navigator.tenant_custom_domains.cert_expires_at END,
    last_error = CASE WHEN navigator.tenant_custom_domains.status = 'tearing_down'
                      THEN NULL ELSE navigator.tenant_custom_domains.last_error END,
    updated_at = NOW()
RETURNING tenant_id, domain, status, acme_dns_subdomain, issuer_id,
          last_verified_at, cert_issued_at, cert_expires_at, last_error,
          created_at, updated_at;

-- name: GetTenantCustomDomain :one
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);

-- name: ListTenantCustomDomainsByStatus :many
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE status = ANY(sqlc.arg(statuses)::text[])
ORDER BY updated_at ASC;

-- name: ListTenantCustomDomains :many
SELECT tenant_id, domain, status, acme_dns_subdomain, issuer_id,
       last_verified_at, cert_issued_at, cert_expires_at, last_error,
       created_at, updated_at
FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY domain ASC;

-- name: SetTenantCustomDomainStatus :execrows
UPDATE navigator.tenant_custom_domains
SET status = sqlc.arg(status),
    last_verified_at = CASE WHEN sqlc.arg(status) = 'verified' THEN NOW() ELSE last_verified_at END,
    cert_issued_at = CASE WHEN sqlc.arg(status) = 'cert_issued' THEN NOW() ELSE cert_issued_at END,
    last_error = NULLIF(sqlc.arg(err_msg)::text, ''),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND domain = sqlc.arg(domain)
  AND (sqlc.arg(status) = 'tearing_down' OR status = sqlc.arg(expected_status));

-- name: SetTenantCustomDomainCertMetadata :execrows
UPDATE navigator.tenant_custom_domains
SET issuer_id = NULLIF(sqlc.arg(issuer_id)::text, ''),
    cert_expires_at = sqlc.narg(cert_expires_at),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND domain = sqlc.arg(domain)
  AND status = sqlc.arg(expected_status);

-- name: FinalizeTenantCustomDomainRemoval :execrows
WITH teardown_authority AS MATERIALIZED (
    SELECT custom_domain.tenant_id, custom_domain.domain
    FROM navigator.tenant_custom_domains AS custom_domain
    WHERE custom_domain.tenant_id = sqlc.arg(tenant_id)::uuid
      AND custom_domain.domain = sqlc.arg(domain)
      AND custom_domain.status = 'tearing_down'
    FOR UPDATE
), deleted_certificate AS (
    DELETE FROM navigator.certificates AS certificate
    USING teardown_authority
    WHERE certificate.tenant_id = teardown_authority.tenant_id
      AND certificate.domain = teardown_authority.domain
), deleted_accounts AS (
    DELETE FROM navigator.acme_accounts AS account
    USING teardown_authority
    WHERE account.tenant_id = teardown_authority.tenant_id
      AND NOT EXISTS (
          SELECT 1
          FROM navigator.tenant_custom_domains AS other_domain
          WHERE other_domain.tenant_id = teardown_authority.tenant_id
            AND other_domain.domain <> teardown_authority.domain
            AND other_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
      )
)
DELETE FROM navigator.tenant_custom_domains AS custom_domain
USING teardown_authority
WHERE custom_domain.tenant_id = teardown_authority.tenant_id
  AND custom_domain.domain = teardown_authority.domain
  AND custom_domain.status = 'tearing_down';

-- name: DeleteTenantCustomDomain :exec
DELETE FROM navigator.tenant_custom_domains
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND domain = sqlc.arg(domain);
