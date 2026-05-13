-- Tenant-owned custom domain state. Driven by Quartermaster signalling
-- Navigator.EnsureCustomDomain when tenants.custom_domain changes on a
-- paid tenant. Navigator runs ACME-DNS-01 delegation: the customer
-- CNAMEs `_acme-challenge.media.acme-inc.com` at
-- `{acme_dns_subdomain}.acme-dns.{root}` so lego writes the validation
-- TXT into a Navigator-owned subzone without ever touching customer DNS.
-- Schema source of truth: pkg/database/sql/schema/navigator.sql

CREATE TABLE IF NOT EXISTS navigator.tenant_custom_domains (
    tenant_id UUID NOT NULL,
    domain TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending_verification',
    acme_dns_subdomain TEXT NOT NULL,
    issuer_id TEXT,
    last_verified_at TIMESTAMPTZ,
    cert_issued_at TIMESTAMPTZ,
    cert_expires_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, domain),
    CONSTRAINT uq_tenant_custom_domains_domain UNIQUE (domain)
);

CREATE INDEX IF NOT EXISTS idx_tenant_custom_domains_status
    ON navigator.tenant_custom_domains(status);
