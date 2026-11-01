CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mollie_unbound_funding_listing
ON purser.payment_provider_intents (created_at ASC, id ASC)
WHERE provider = 'mollie' AND purpose = 'mollie_first_payment'
  AND status = 'succeeded' AND amount_cents > 0
  AND provider_payment_id IS NOT NULL AND provider_mandate_id IS NULL
  AND activated_at IS NULL AND recovery_started_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mollie_unbound_funding_tenant
ON purser.payment_provider_intents (tenant_id ASC, created_at ASC, id ASC)
WHERE provider = 'mollie' AND purpose = 'mollie_first_payment'
  AND status = 'succeeded' AND amount_cents > 0
  AND provider_payment_id IS NOT NULL AND provider_mandate_id IS NULL
  AND activated_at IS NULL AND recovery_started_at IS NULL;
