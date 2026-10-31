ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS free_fallback_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_reason VARCHAR(24),
    ADD COLUMN IF NOT EXISTS free_fallback_notified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_moved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_attempted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_failures INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS free_fallback_error TEXT,
    ADD COLUMN IF NOT EXISTS free_fallback_notice_attempted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_notice_failures INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS free_fallback_notice_error TEXT,
    ADD COLUMN IF NOT EXISTS free_fallback_notice_abandoned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_provider_stopping BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS free_fallback_wait_since TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_unbilled_through TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS free_fallback_provider_ended_at TIMESTAMPTZ;

ALTER TABLE purser.tenant_subscriptions
    DROP CONSTRAINT IF EXISTS chk_subscription_free_fallback_reason;

ALTER TABLE purser.tenant_subscriptions
    ADD CONSTRAINT chk_subscription_free_fallback_reason
    CHECK (free_fallback_reason IN ('payment_failed', 'cancelled')) NOT VALID;
