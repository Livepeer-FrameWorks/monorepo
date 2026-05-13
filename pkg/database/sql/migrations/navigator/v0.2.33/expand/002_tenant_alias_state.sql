-- Tenant alias state machine. Two tables drive per-tenant
-- alias lifecycle and per-edge cert apply state:
--   - navigator.tenant_aliases: alias intent + ACME state per tenant.
--     Quartermaster signals via Navigator.EnsureTenantAlias on paid tier
--     activation; Navigator does the ACME work async.
--   - navigator.tenant_edge_apply_state: per (tenant, edge, bundle)
--     state. Drives DNS membership: only 'applied' edges enter the
--     tenant's smart record set in cdn.{root}.
-- Schema source of truth: pkg/database/sql/schema/navigator.sql

CREATE TABLE IF NOT EXISTS navigator.tenant_aliases (
    tenant_id UUID PRIMARY KEY,
    subdomain TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'cert_issuing',
    cert_issued_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tenant_aliases_subdomain UNIQUE (subdomain)
);

CREATE INDEX IF NOT EXISTS idx_tenant_aliases_status ON navigator.tenant_aliases(status);

CREATE TABLE IF NOT EXISTS navigator.tenant_edge_apply_state (
    tenant_id UUID NOT NULL,
    cluster_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    bundle_id TEXT NOT NULL,
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
