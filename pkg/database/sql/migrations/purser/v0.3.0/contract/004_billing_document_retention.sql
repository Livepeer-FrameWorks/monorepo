-- Finalize document invariants after postdeploy/002 has backfilled every row.

ALTER TABLE purser.billing_invoices
    ALTER COLUMN invoice_number SET NOT NULL,
    ALTER COLUMN retention_until SET NOT NULL;

ALTER TABLE purser.billing_payments
    ALTER COLUMN retention_until SET NOT NULL;

ALTER TABLE purser.simplified_invoices
    ALTER COLUMN retention_until SET NOT NULL;

ALTER TABLE purser.credit_notes
    ALTER COLUMN retention_until SET NOT NULL;
