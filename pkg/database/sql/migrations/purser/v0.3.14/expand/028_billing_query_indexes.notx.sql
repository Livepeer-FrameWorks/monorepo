CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prepaid_funding_lots_tenant_source_document
ON purser.prepaid_funding_lots(tenant_id,source_document_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prepaid_funding_lots_active_remaining
ON purser.prepaid_funding_lots(tenant_id) INCLUDE(remaining_net_eur_cents)
WHERE remaining_net_eur_cents>0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_balance_transactions_invoice_credit_key
ON purser.balance_transactions(tenant_id,description,id)
WHERE reference_type='invoice_credit';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoice_phase_close_latest
ON purser.billing_invoices(tenant_id,period_end DESC NULLS LAST)
WHERE (document_kind='prepaid_statement' AND usage_details->'statement'->>'closes_prepaid_phase'='true')
 OR (document_kind='invoice' AND usage_details->>'closes_postpaid_phase'='true');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_simplified_invoice_tenant_reference_lower
ON purser.simplified_invoices(tenant_id,reference_type,LOWER(reference_id));
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_crypto_invoice_tenant_reference_lower
ON purser.crypto_invoices(tenant_id,reference_type,LOWER(reference_id));
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoice_tenant_reference_lower
ON purser.billing_invoices(tenant_id,source_reference_type,LOWER(source_reference_id))
WHERE tax_status='ready';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_usage_credit_allocation
ON purser.provider_usage_records(usage_tenant_id,work_cluster_id,usage_type,period_start,period_end)
WHERE value_kind='delta' AND granularity='minute_5' AND usage_value<>0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_adjustment_credit_allocation
ON purser.usage_adjustments(tenant_id,cluster_id,usage_type,period_start,period_end)
WHERE value_kind='correction_delta' AND status='applied' AND delta_value<>0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cluster_subscription_stripe_owner
ON purser.cluster_subscriptions(stripe_customer_id ASC,tenant_id ASC)
WHERE stripe_customer_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenant_subscription_stripe_owner
ON purser.tenant_subscriptions(stripe_customer_id ASC,tenant_id ASC)
WHERE stripe_customer_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_intent_stripe_owner
ON purser.payment_provider_intents(provider_customer_id ASC,tenant_id ASC)
WHERE provider='stripe' AND provider_customer_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_confirmed_invoice
ON purser.billing_payments(invoice_id,currency)
INCLUDE(amount,reversed_amount_cents,tenant_id)
WHERE status='confirmed';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_owner_verification_range
ON purser.billing_payments(id ASC) INCLUDE(invoice_id,tenant_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoices_overlap_end
ON purser.billing_invoices(tenant_id,base_fee_period_end,base_fee_period_start DESC,id)
WHERE base_fee_period_start IS NOT NULL AND base_fee_period_end IS NOT NULL
 AND usage_details->'unused_share_returned' IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoices_document_time
ON purser.billing_invoices(tenant_id,COALESCE(finalized_at,created_at) DESC,id DESC)
WHERE status<>'draft' AND COALESCE(finalized_at,created_at) IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoices_document_unknown_time
ON purser.billing_invoices(tenant_id,id DESC)
WHERE status<>'draft' AND finalized_at IS NULL AND created_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_document_time
ON purser.billing_payments(tenant_id,COALESCE(confirmed_at,created_at) DESC,id DESC)
WHERE status='confirmed' AND COALESCE(confirmed_at,created_at) IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_document_unknown_time
ON purser.billing_payments(tenant_id,id DESC)
WHERE status='confirmed' AND confirmed_at IS NULL AND created_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_simplified_invoices_document_time
ON purser.simplified_invoices(tenant_id,issued_at DESC,id DESC)
WHERE tax_validation_status<>'location_review';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_crypto_invoices_document_time
ON purser.crypto_invoices(tenant_id,issued_at DESC,id DESC)
WHERE tax_validation_status<>'location_review';
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_credit_notes_document_time
ON purser.credit_notes(tenant_id,issued_at DESC,id DESC);
