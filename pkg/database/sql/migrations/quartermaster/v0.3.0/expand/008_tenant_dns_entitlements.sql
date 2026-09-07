ALTER TABLE quartermaster.tenants
    ADD COLUMN IF NOT EXISTS custom_subdomain_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS custom_domain_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS billing_entitlements_observed_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch';

CREATE TABLE IF NOT EXISTS quartermaster.billing_entitlement_handoffs (
    handoff_key TEXT PRIMARY KEY,
    subscription_count BIGINT NOT NULL CHECK (subscription_count >= 0),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
