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
    -- Tracks which CA issued the cert ('letsencrypt' | 'google-trust').
    -- Renewals must hit the same CA the cert was originally issued under.
    issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt',
    -- Unique constraint: same domain can exist for different tenants (or platform-wide if tenant_id is NULL)
    CONSTRAINT idx_certificates_tenant_domain UNIQUE (tenant_id, domain)
);

-- Idempotent: add issuer_ca for environments where the table existed
-- before this column.
ALTER TABLE navigator.certificates
    ADD COLUMN IF NOT EXISTS issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt';

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
    registration_json TEXT NOT NULL, -- Serialized ACME registration (CA-specific account URL)
    private_key_pem TEXT NOT NULL, -- AES-256-GCM encrypted via pkg/crypto.FieldEncryptor (enc:v1: prefix)
    created_at TIMESTAMPTZ DEFAULT NOW(),
    ca TEXT NOT NULL DEFAULT 'letsencrypt',
    CONSTRAINT idx_acme_accounts_tenant_email_ca UNIQUE (tenant_id, email, ca)
);

-- Per-(tenant, email, ca) uniqueness. ACME registrations are CA-specific;
-- the registration JWS binds an account key to one directory URL, so
-- reusing a letsencrypt registration JSON against the google-trust
-- directory will fail. Renewals must reuse the same account at the
-- same CA. See logic/cert.go getOrCreateUser.
ALTER TABLE navigator.acme_accounts
    ADD COLUMN IF NOT EXISTS ca TEXT NOT NULL DEFAULT 'letsencrypt';

-- Index for efficient tenant lookups
CREATE INDEX IF NOT EXISTS idx_acme_accounts_tenant ON navigator.acme_accounts(tenant_id);
-- Ensure platform-wide ACME accounts are unique per (email, ca)
CREATE UNIQUE INDEX IF NOT EXISTS idx_acme_accounts_platform_email_ca
    ON navigator.acme_accounts(email, ca)
    WHERE tenant_id IS NULL;

CREATE TABLE IF NOT EXISTS navigator.tls_bundles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bundle_id TEXT NOT NULL UNIQUE,
    domains JSONB NOT NULL DEFAULT '[]'::jsonb,
    cert_pem TEXT NOT NULL,
    key_pem TEXT NOT NULL, -- AES-256-GCM encrypted via pkg/crypto.FieldEncryptor (enc:v1: prefix)
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt'
);

-- Idempotent column add for environments that pre-date per-CA renewal pinning.
ALTER TABLE navigator.tls_bundles
    ADD COLUMN IF NOT EXISTS issuer_ca TEXT NOT NULL DEFAULT 'letsencrypt';

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

-- Tenant alias intent. Quartermaster signals via Navigator.EnsureTenantAlias
-- on paid tier activation; Navigator persists the row and works the alias
-- asynchronously (ACME issuance + DNS publish).
CREATE TABLE IF NOT EXISTS navigator.tenant_aliases (
    tenant_id UUID PRIMARY KEY,
    -- Subdomain label inside the tenant-alias zone (e.g. "acme" → acme.cdn.frameworks.network)
    subdomain TEXT NOT NULL,
    -- Lifecycle: cert_issuing | cert_issued | cert_failed | tearing_down
    status TEXT NOT NULL DEFAULT 'cert_issuing',
    cert_issued_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tenant_aliases_subdomain UNIQUE (subdomain)
);

CREATE INDEX IF NOT EXISTS idx_tenant_aliases_status ON navigator.tenant_aliases(status);

-- Per-edge bundle apply state. Drives DNS membership decisions: only
-- edges with acknowledged tenant TLS bundles are added to a tenant's
-- Bunny smart record set in cdn.{root}. Populated from Foghorn's
-- ConfigSeedApplyResult reports.
CREATE TABLE IF NOT EXISTS navigator.tenant_edge_apply_state (
    tenant_id UUID NOT NULL,
    cluster_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    -- e.g. "tenant:{tenant_id}"; matches the bundle_id Foghorn pushed.
    bundle_id TEXT NOT NULL,
    -- Lifecycle: pending_distribute | pending_apply | applied | in_dns
    state TEXT NOT NULL DEFAULT 'pending_distribute',
    last_seed_version BIGINT,
    last_ack_at TIMESTAMPTZ,
    in_dns_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, node_id, bundle_id)
);

CREATE INDEX IF NOT EXISTS idx_tenant_edge_apply_state_tenant
    ON navigator.tenant_edge_apply_state(tenant_id, state);
CREATE INDEX IF NOT EXISTS idx_tenant_edge_apply_state_cluster
    ON navigator.tenant_edge_apply_state(cluster_id);
