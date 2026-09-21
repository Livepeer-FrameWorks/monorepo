-- The prepaid ledger and invoice totals are EUR only, spelled in uppercase.
-- The purser_eur_ledger_conversion_v0_3_11 data migration has converted or
-- folded every other balance row before this phase runs.
ALTER TABLE purser.prepaid_balances
    DROP CONSTRAINT IF EXISTS chk_prepaid_balances_ledger_currency,
    ADD CONSTRAINT chk_prepaid_balances_ledger_currency CHECK (currency = 'EUR');

ALTER TABLE purser.billing_invoices
    DROP CONSTRAINT IF EXISTS chk_billing_invoices_ledger_currency,
    ADD CONSTRAINT chk_billing_invoices_ledger_currency CHECK (currency = 'EUR');
