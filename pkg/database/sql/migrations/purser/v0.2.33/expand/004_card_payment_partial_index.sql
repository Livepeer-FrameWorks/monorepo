-- Allow multiple provider-backed card payments for one invoice while keeping
-- idempotency for locally-created checkout sessions that do not have a tx_id
-- yet. Mollie base subscription and overage payments can both be pending for
-- the same invoice; each is uniquely identified by tx_id once the provider
-- payment exists.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_payments_pending_invoice_method
    ON purser.billing_payments(invoice_id, method)
    WHERE status = 'pending' AND tx_id IS NULL;
