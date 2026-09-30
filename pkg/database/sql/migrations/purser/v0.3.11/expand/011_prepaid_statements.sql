-- A prepaid statement states a prepaid period: its usage at rated prices, what
-- the prepaid balance paid for it, top-ups and balances. The balance already
-- paid for it, so a statement is never payable and never holds invoice credit.
ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS document_kind VARCHAR(20) NOT NULL DEFAULT 'invoice';
ALTER TABLE purser.billing_invoices
    DROP CONSTRAINT IF EXISTS chk_billing_invoices_document_kind,
    ADD CONSTRAINT chk_billing_invoices_document_kind CHECK (
        document_kind = 'invoice'
        OR (document_kind = 'prepaid_statement' AND amount = 0 AND prepaid_credit_applied = 0)
    ) NOT VALID;
