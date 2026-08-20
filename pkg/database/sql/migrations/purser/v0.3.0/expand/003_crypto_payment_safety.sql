-- Preserve confirmed crypto receipts that cannot be allocated automatically.
-- Quote expiry ends automatic pricing; it never ends custody observation.

ALTER TABLE purser.crypto_wallets
    ADD CONSTRAINT chk_wallet_status_v3_compat CHECK (
        status IN ('pending', 'confirming', 'review_required', 'completed', 'swept', 'expired')
    ) NOT VALID;
ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS chk_wallet_status;

CREATE SEQUENCE IF NOT EXISTS purser.simplified_invoice_number_seq;

ALTER TABLE purser.x402_nonces
    ADD COLUMN IF NOT EXISTS client_ip INET,
    ADD COLUMN IF NOT EXISTS rollup_applied_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS rollup_reversed_at TIMESTAMPTZ;

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS vat_rate_source VARCHAR(255),
    ADD COLUMN IF NOT EXISTS vat_rate_table_checked_on DATE,
    ADD COLUMN IF NOT EXISTS tax_validation_status VARCHAR(32) NOT NULL DEFAULT 'not_validated';
