CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_credit_clawback_origin
    ON purser.operator_credit_ledger (
        reverses_ledger_id ASC
    )
    INCLUDE (gross_cents, platform_fee_cents)
    WHERE entry_type = 'clawback';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grants_failed
    ON purser.subscription_operator_grants (
        expiry_failures ASC,
        subscription_id ASC
    )
    WHERE expiry_error IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_fallback_due_retry
    ON purser.tenant_subscriptions (
        free_fallback_at ASC,
        tenant_id ASC
    )
    INCLUDE (id, free_fallback_attempted_at)
    WHERE free_fallback_at IS NOT NULL
      AND free_fallback_moved_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_fallback_notice_retry
    ON purser.tenant_subscriptions (
        free_fallback_notice_attempted_at ASC NULLS FIRST,
        free_fallback_at ASC,
        tenant_id ASC
    )
    WHERE free_fallback_at IS NOT NULL
      AND (free_fallback_moved_at IS NOT NULL OR free_fallback_notified_at IS NULL);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_fallback_failed
    ON purser.tenant_subscriptions (
        free_fallback_failures ASC,
        tenant_id ASC
    )
    INCLUDE (id)
    WHERE free_fallback_at IS NOT NULL
      AND free_fallback_moved_at IS NULL
      AND free_fallback_error IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_fallback_wait
    ON purser.tenant_subscriptions (
        free_fallback_wait_since ASC,
        tenant_id ASC
    )
    INCLUDE (id)
    WHERE free_fallback_at IS NOT NULL
      AND free_fallback_moved_at IS NULL
      AND free_fallback_wait_since IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_fallback_notice_failed
    ON purser.tenant_subscriptions (
        free_fallback_notice_failures ASC,
        tenant_id ASC
    )
    WHERE free_fallback_at IS NOT NULL
      AND free_fallback_notice_error IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_failed_attempts
    ON purser.provider_webhook_inbox (
        attempts ASC,
        id ASC
    )
    WHERE status IN ('pending', 'failed', 'processing');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_unresolved_age
    ON purser.provider_webhook_inbox (
        unresolved_since ASC,
        id ASC
    )
    WHERE status IN ('pending', 'failed', 'processing')
      AND unresolved_since IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_recovery_listing
    ON purser.provider_webhook_inbox (
        created_at ASC,
        id ASC
    )
    WHERE status <> 'processed';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_recovery_tenant
    ON purser.provider_webhook_inbox (
        (headers->>'tenant_id') ASC,
        created_at ASC,
        id ASC
    )
    WHERE status <> 'processed';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_stripe_customer_latest
    ON purser.tenant_subscriptions (
        stripe_customer_id ASC,
        created_at DESC
    )
    INCLUDE (tenant_id)
    WHERE stripe_customer_id IS NOT NULL;

