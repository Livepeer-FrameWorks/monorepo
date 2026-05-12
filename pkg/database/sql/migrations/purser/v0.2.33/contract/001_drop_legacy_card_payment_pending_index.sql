-- Contract cleanup for the pre-v0.2.33 pending card-payment uniqueness index.
-- The expand migration adds the replacement partial index; dropping the old
-- stricter index is destructive for old binaries and therefore belongs here.

DROP INDEX IF EXISTS purser.idx_purser_billing_payments_pending_invoice_method;
