-- v0.3.5: exact per-domain certificates for tenant custom domains have no
-- consumer; edges receive custom domains only as tenant bundle SANs. Platform
-- certificates and tenant certificates for other domains are untouched.

DELETE FROM navigator.certificates AS certificate
USING navigator.tenant_custom_domains AS custom_domain
WHERE certificate.tenant_id = custom_domain.tenant_id
  AND certificate.domain = custom_domain.domain;
