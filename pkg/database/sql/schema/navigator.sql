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

-- Retired alias labels awaiting Bunny record cleanup. tenant_aliases is
-- keyed by tenant_id and overwrites subdomain in place on a rename, so it
-- has no memory of the old label. Quartermaster enqueues a retirement via
-- Navigator.RemoveTenantAliasSubdomain for the old label; the alias worker
-- clears that label's apex + per-service records and deletes the row.
-- requested_at lets the worker drop a stale retirement when the label was
-- re-pointed back to the tenant (the a -> b -> a case).
CREATE TABLE IF NOT EXISTS navigator.tenant_alias_retirements (
    tenant_id    UUID NOT NULL,
    subdomain    TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    PRIMARY KEY (tenant_id, subdomain)
);

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

-- Tenant-owned custom domain (`media.acme-inc.com`). Quartermaster signals
-- via Navigator.EnsureCustomDomain when tenants.custom_domain changes on a
-- paid tenant. Navigator runs the verification + cert-issuance state machine
-- through ACME-DNS-01 delegation: the customer points
-- `media.acme-inc.com` CNAME at `{tenant_subdomain}.cdn.{root}` for traffic,
-- and `_acme-challenge.media.acme-inc.com` CNAME at
-- `{acme_dns_subdomain}.acme-dns.{root}` so lego can write the challenge
-- TXT into a Navigator-owned zone (no need for Navigator to touch the
-- customer's DNS).
CREATE TABLE IF NOT EXISTS navigator.tenant_custom_domains (
    tenant_id UUID NOT NULL,
    -- Customer-owned FQDN, e.g. "media.acme-inc.com". Lowercased + DNS-safe.
    domain TEXT NOT NULL,
    -- Lifecycle:
    --   pending_verification   waiting for customer CNAMEs to point at platform
    --   verified               CNAMEs verified; cert issuance queued
    --   cert_issuing           ACME order in flight
    --   cert_issued            cert active; ready for distribution to edges
    --   cert_failed            ACME failed; manual intervention
    --   tearing_down           remove requested; worker clearing state
    status TEXT NOT NULL DEFAULT 'pending_verification',
    -- Stable Navigator-owned subdomain for ACME-DNS-01 delegation. The
    -- customer CNAMEs _acme-challenge.{domain} → {acme_dns_subdomain}.acme-dns.{root}
    -- once and never has to touch it again. Each tenant_custom_domain gets
    -- a fresh random slug so revoking one domain doesn't strand a shared
    -- challenge path.
    acme_dns_subdomain TEXT NOT NULL,
    -- Issuer chosen at issuance time; persisted so renewals stay on the
    -- same CA unless an operator-driven migration moves them.
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

-- Schema baseline identity marker. Records that this database was created from the
-- consolidated baseline at this floor, so the migration min-version guard treats
-- below-floor migrations as folded into the baseline (not missing). An existing
-- cluster upgraded in place has no marker and is checked for ledger completeness
-- instead. The floor value is kept in sync with provisioner.schemaMigrationBaselineFloor
-- by TestBaselineMarkerFloorMatchesConst. See docs/standards/schema-migrations.md.
CREATE TABLE IF NOT EXISTS public._schema_baseline (
    floor TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
    SELECT 'v0.2.96' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
