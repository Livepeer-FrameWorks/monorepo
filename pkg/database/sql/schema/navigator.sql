CREATE SCHEMA IF NOT EXISTS navigator;

CREATE TABLE IF NOT EXISTS navigator.certificates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID, -- Optional: NULL for platform certificates, set for tenant subdomains (platform-managed)
    domain TEXT NOT NULL,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL, -- AES-256-GCM encrypted via pkg/crypto.FieldEncryptor (enc:v1: prefix)
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    -- Unique constraint: same domain can exist for different tenants (or platform-wide if tenant_id is NULL)
    CONSTRAINT idx_certificates_tenant_domain UNIQUE (tenant_id, domain)
);

-- Index for efficient tenant lookups
CREATE INDEX IF NOT EXISTS idx_certificates_tenant ON navigator.certificates(tenant_id);
-- Ensure platform-wide certs are unique (NULL tenant_id does not enforce uniqueness)
CREATE UNIQUE INDEX IF NOT EXISTS idx_certificates_platform_domain
    ON navigator.certificates(domain)
    WHERE tenant_id IS NULL;

CREATE TABLE IF NOT EXISTS navigator.acme_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID, -- Optional: NULL for platform accounts, set for tenant-specific ACME accounts
    email TEXT NOT NULL,
    registration_json TEXT NOT NULL, -- Serialized ACME registration
    private_key_pem TEXT NOT NULL, -- AES-256-GCM encrypted via pkg/crypto.FieldEncryptor (enc:v1: prefix)
    created_at TIMESTAMPTZ DEFAULT NOW(),
    -- Unique constraint: same email per tenant (or platform-wide if tenant_id is NULL)
    CONSTRAINT idx_acme_accounts_tenant_email UNIQUE (tenant_id, email)
);

-- Index for efficient tenant lookups
CREATE INDEX IF NOT EXISTS idx_acme_accounts_tenant ON navigator.acme_accounts(tenant_id);
-- Ensure platform-wide ACME accounts are unique
CREATE UNIQUE INDEX IF NOT EXISTS idx_acme_accounts_platform_email
    ON navigator.acme_accounts(email)
    WHERE tenant_id IS NULL;

CREATE TABLE IF NOT EXISTS navigator.tls_bundles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bundle_id TEXT NOT NULL UNIQUE,
    domains JSONB NOT NULL DEFAULT '[]'::jsonb,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL, -- AES-256-GCM encrypted via pkg/crypto.FieldEncryptor (enc:v1: prefix)
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tls_bundles_expires_at ON navigator.tls_bundles(expires_at);

CREATE TABLE IF NOT EXISTS navigator.internal_ca (
    role TEXT PRIMARY KEY,
    cert_pem TEXT NOT NULL,
    key_pem TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT chk_internal_ca_role CHECK (role IN ('root_cert_only', 'intermediate'))
);

CREATE TABLE IF NOT EXISTS navigator.internal_certificates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id TEXT NOT NULL,
    cluster_id TEXT NOT NULL,
    service_type TEXT NOT NULL,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT uq_internal_certificates_node_service UNIQUE (node_id, service_type)
);

CREATE INDEX IF NOT EXISTS idx_internal_certificates_expires_at ON navigator.internal_certificates(expires_at);
