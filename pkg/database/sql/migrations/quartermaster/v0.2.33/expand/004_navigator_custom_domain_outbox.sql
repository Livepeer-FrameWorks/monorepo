-- Durable outbox for Navigator custom-domain lifecycle hooks. UpdateTenant
-- inserts a row in the same tx as the quartermaster.tenants UPDATE, so a
-- Navigator outage cannot leave QM saying "tenant has custom_domain" while
-- Navigator never created the verification + cert lifecycle row. The drain
-- worker calls Navigator.EnsureCustomDomain / RemoveCustomDomain with
-- exponential backoff until the action lands.
--
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

CREATE TABLE IF NOT EXISTS quartermaster.navigator_custom_domain_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    domain    TEXT NOT NULL,
    action    TEXT NOT NULL CHECK (action IN ('ensure', 'remove')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_qm_navigator_custom_domain_outbox_pending
    ON quartermaster.navigator_custom_domain_outbox(created_at)
    WHERE completed_at IS NULL;
