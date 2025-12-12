CREATE SCHEMA IF NOT EXISTS navigator;

CREATE TABLE IF NOT EXISTS navigator.certificates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID, -- Optional: NULL for platform certificates, set for tenant custom domains
    domain TEXT NOT NULL,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL, -- Encrypted at rest (future) or restricted access
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    -- Unique constraint: same domain can exist for different tenants (or platform-wide if tenant_id is NULL)
    CONSTRAINT idx_certificates_tenant_domain UNIQUE (tenant_id, domain)
);

-- Index for efficient tenant lookups
CREATE INDEX IF NOT EXISTS idx_certificates_tenant ON navigator.certificates(tenant_id);

CREATE TABLE IF NOT EXISTS navigator.acme_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID, -- Optional: NULL for platform accounts, set for tenant-specific ACME accounts
    email TEXT NOT NULL,
    registration_json TEXT NOT NULL, -- Serialized ACME registration
    private_key_pem TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    -- Unique constraint: same email per tenant (or platform-wide if tenant_id is NULL)
    CONSTRAINT idx_acme_accounts_tenant_email UNIQUE (tenant_id, email)
);

-- Index for efficient tenant lookups
CREATE INDEX IF NOT EXISTS idx_acme_accounts_tenant ON navigator.acme_accounts(tenant_id);
