-- v0.3.11: an operator's billing arrangement for one subscription (base fee
-- override, usage waiver, manual invoice collection, expiry).
CREATE TABLE IF NOT EXISTS purser.subscription_operator_grants (
    subscription_id UUID PRIMARY KEY REFERENCES purser.tenant_subscriptions(id) ON DELETE CASCADE,
    base_price NUMERIC(10,2),
    waive_usage BOOLEAN NOT NULL DEFAULT false,
    collection VARCHAR(16) NOT NULL DEFAULT 'provider',
    expires_at TIMESTAMPTZ,
    reason TEXT NOT NULL,
    granted_by TEXT,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_subscription_operator_grant_collection CHECK (collection IN ('provider', 'invoice')),
    CONSTRAINT chk_subscription_operator_grant_base_price CHECK (base_price IS NULL OR base_price >= 0)
);
