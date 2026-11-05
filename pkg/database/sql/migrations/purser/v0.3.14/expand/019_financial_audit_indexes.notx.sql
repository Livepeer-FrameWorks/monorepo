CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_invoices_financial_audit
ON purser.billing_invoices(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_payments_financial_audit
ON purser.billing_payments(invoice_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_simplified_invoices_financial_audit
ON purser.simplified_invoices(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_crypto_invoices_financial_audit
ON purser.crypto_invoices(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_credit_notes_financial_audit
ON purser.credit_notes(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_settlements_financial_audit
ON purser.provider_settlements(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pending_topups_financial_audit
ON purser.pending_topups(tenant_id, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_balance_transactions_financial_audit
ON purser.balance_transactions(tenant_id, id);
