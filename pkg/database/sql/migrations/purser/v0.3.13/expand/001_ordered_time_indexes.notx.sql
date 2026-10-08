CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_delegated_jwt_replays_expires_at_range
    ON purser.delegated_jwt_replays (expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenant_subscriptions_pending_due_range
    ON purser.tenant_subscriptions(pending_effective_at ASC)
    WHERE pending_tier_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_usage_records_created_at_range ON purser.usage_records(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stripe_meter_events_outbox_pending_range
    ON purser.stripe_meter_events_outbox(created_at ASC)
    WHERE sent_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invoice_email_outbox_pending_range
    ON purser.invoice_email_outbox(next_attempt_at ASC, created_at)
    WHERE sent_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x402_nonces_submitting_range ON purser.x402_nonces(settled_at ASC) WHERE status = 'submitting';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_crypto_accounting_anomalies_open_range
    ON purser.crypto_accounting_anomalies(last_seen_at ASC, tenant_id)
    WHERE status = 'open';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x402_rate_limit_windows_expiry_range
    ON purser.x402_rate_limit_windows(window_started_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_x402_mutation_results_unknown_range
    ON purser.x402_mutation_results(claimed_at ASC)
	WHERE status IN ('claimed', 'completion_pending', 'operator_review');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_simplified_invoices_issued_range ON purser.simplified_invoices(issued_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenant_balance_rollups_last_topup_range ON purser.tenant_balance_rollups(last_topup_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payment_attempts_next_retry_range
    ON purser.billing_payment_attempts(next_retry_at ASC, status)
    WHERE next_retry_at IS NOT NULL AND status = 'provider_call_failed';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_webhook_inbox_pending_range
    ON purser.provider_webhook_inbox(next_attempt_at ASC, created_at)
    WHERE status IN ('pending', 'failed', 'processing');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_billing_event_outbox_pending_range
    ON purser.billing_event_outbox(created_at ASC)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_domain_event_outbox_pending_range
    ON purser.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_domain_event_outbox_completed_range
    ON purser.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_tenant_subscriptions_billing_date_range ON purser.tenant_subscriptions(next_billing_date ASC);
