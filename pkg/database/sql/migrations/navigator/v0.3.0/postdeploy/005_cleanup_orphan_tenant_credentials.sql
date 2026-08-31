DELETE FROM navigator.certificates AS certificate
WHERE certificate.tenant_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM navigator.tenant_custom_domains AS custom_domain
      WHERE custom_domain.tenant_id = certificate.tenant_id
        AND custom_domain.domain = certificate.domain
        AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
  );

DELETE FROM navigator.tls_bundles AS bundle
WHERE bundle.bundle_id LIKE 'tenant:%'
  AND NOT EXISTS (
      SELECT 1
      FROM navigator.tenant_aliases AS alias
      WHERE bundle.bundle_id = 'tenant:' || alias.tenant_id::text
  );

DELETE FROM navigator.acme_accounts AS account
WHERE account.tenant_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM navigator.tenant_custom_domains AS custom_domain
      WHERE custom_domain.tenant_id = account.tenant_id
        AND custom_domain.status IN ('verified', 'cert_issuing', 'cert_issued', 'cert_failed')
  );
