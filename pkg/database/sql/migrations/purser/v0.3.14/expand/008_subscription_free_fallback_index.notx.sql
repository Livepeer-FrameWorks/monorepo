CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenant_subscriptions_free_fallback
    ON purser.tenant_subscriptions(free_fallback_at ASC)
    WHERE free_fallback_at IS NOT NULL;
