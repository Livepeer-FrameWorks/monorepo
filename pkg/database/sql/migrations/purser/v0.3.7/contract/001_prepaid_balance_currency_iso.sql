-- Prepaid balances are keyed by the uppercase ISO 4217 code that every reader
-- queries; the v0.3.7 postdeploy merge removed non-uppercase rows.

ALTER TABLE purser.prepaid_balances
    DROP CONSTRAINT IF EXISTS chk_prepaid_balances_currency_iso;
ALTER TABLE purser.prepaid_balances
    ADD CONSTRAINT chk_prepaid_balances_currency_iso CHECK (currency ~ '^[A-Z]{3}$');
