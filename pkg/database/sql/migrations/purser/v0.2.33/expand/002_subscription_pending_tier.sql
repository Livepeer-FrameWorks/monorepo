-- Stage a scheduled tier change (typically a downgrade applied at period close).
-- pending_tier_id != tier_id signals the post-commit applier in
-- api_billing/internal/handlers/jobs.go to flip the tier and reconcile cluster
-- access. Upgrades are applied immediately and never use these columns.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS pending_tier_id UUID REFERENCES purser.billing_tiers(id),
    ADD COLUMN IF NOT EXISTS pending_effective_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS pending_reason VARCHAR(50);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_pending_due
    ON purser.tenant_subscriptions(pending_effective_at)
    WHERE pending_tier_id IS NOT NULL;
