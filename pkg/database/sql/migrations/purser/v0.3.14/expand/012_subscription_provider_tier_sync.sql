-- pending_provider_tier_id names the scheduled downgrade target the recurring
-- provider subscription has confirmed. provider_tier_changed_at orders Stripe
-- events against the latest tier Purser wrote to that subscription.
ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS pending_provider_tier_id UUID,
    ADD COLUMN IF NOT EXISTS provider_tier_changed_at TIMESTAMPTZ;
