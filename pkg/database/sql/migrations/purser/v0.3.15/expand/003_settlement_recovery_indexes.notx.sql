CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_blocked
    ON purser.provider_webhook_inbox(id ASC) WHERE status = 'blocked';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_bound_recovery_tenant
    ON purser.provider_webhook_inbox((COALESCE(tenant_id::text, headers->>'tenant_id')) ASC, created_at ASC, id ASC)
    WHERE status <> 'processed';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_valuation_queue
    ON purser.provider_settlements(updated_at ASC, id ASC) WHERE status = 'recorded';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_supported_queue
    ON purser.provider_settlements(settlement_currency ASC, updated_at ASC, id ASC)
    WHERE status = 'blocked' AND last_error_code = 'unsupported_currency';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_inbox_blocked_payment
 ON purser.provider_webhook_inbox(provider ASC, tenant_id ASC, provider_payment_id ASC) WHERE status='blocked';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_recovery_listing
 ON purser.provider_settlements(created_at ASC,id ASC) WHERE status IN ('recorded','blocked');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_recovery_tenant
 ON purser.provider_settlements(tenant_id ASC,created_at ASC,id ASC) WHERE status IN ('recorded','blocked');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_recovery_state
 ON purser.provider_settlements(status ASC,settled_at ASC,id ASC) WHERE status IN ('recorded','blocked');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_identity_backfill
 ON purser.provider_settlements(tenant_id ASC, id ASC)
 WHERE status='settled' AND settlement_currency='EUR' AND settled_eur_cents IS NULL;
