CREATE TABLE IF NOT EXISTS purser.subscription_tier_upgrades (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    old_tier_id UUID NOT NULL,
    tier_id UUID NOT NULL,
    actor_id TEXT NOT NULL,
    old_period_start TIMESTAMPTZ NOT NULL,
    old_period_end TIMESTAMPTZ NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL,
    provider TEXT NOT NULL,
    collection_provider TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    provider_subscription_id TEXT NOT NULL,
    stripe_price_id TEXT NOT NULL,
    old_base_cents BIGINT NOT NULL,
    base_cents BIGINT NOT NULL,
    state TEXT NOT NULL DEFAULT 'requested',
    provider_receipt JSONB NOT NULL DEFAULT '{}',
    recovery_requested_at TIMESTAMPTZ,
    recovery_actor_id TEXT,
    recovery_reason TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_tier_upgrade_state CHECK (state IN ('requested','provider_applied','pricing_applied','completed','cancelled')),
    CONSTRAINT chk_tier_upgrade_provider CHECK (provider IN ('stripe','mollie','advance','invoice')),
    CONSTRAINT chk_tier_upgrade_period CHECK (old_period_end > old_period_start)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_subscription_tier_upgrade_pending
    ON purser.subscription_tier_upgrades (tenant_id)
    WHERE state IN ('requested','provider_applied','pricing_applied');
CREATE INDEX IF NOT EXISTS idx_subscription_tier_upgrades_tenant
    ON purser.subscription_tier_upgrades (tenant_id ASC, provider_subscription_id ASC, requested_at DESC);
