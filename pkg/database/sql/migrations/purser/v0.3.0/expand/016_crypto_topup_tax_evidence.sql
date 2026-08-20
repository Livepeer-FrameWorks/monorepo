-- Retain the trusted request address used as one of the customer-location
-- signals for direct crypto prepaid top-up invoicing. Keep the legacy evidence
-- column through expand so old binaries remain compatible during rollout.

ALTER TABLE purser.crypto_wallets
    ADD COLUMN IF NOT EXISTS client_ip VARCHAR(64);

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS tax_policy_ref VARCHAR(255);
