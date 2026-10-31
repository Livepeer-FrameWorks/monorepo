CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_checkout_unresolved
ON purser.payment_provider_intents(created_at ASC, id ASC)
WHERE purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create')
  AND status IN ('pending', 'provider_open', 'provider_call_failed');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_refund_settlement_payment
    ON purser.provider_webhook_inbox ((headers->>'provider') ASC, (headers->>'provider_payment_id') ASC)
    WHERE provider = 'refund_settlement';
