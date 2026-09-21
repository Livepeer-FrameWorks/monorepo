-- Every money row records the amount and currency the payer was charged in,
-- the EUR amount that reached the ledger, the rate applied (units of the
-- original currency per euro), where the rate came from, and its reference
-- date. identity is an EUR amount at rate 1; ecb is an ECB reference rate;
-- legacy_quote is a USD rate quoted before ECB reference rates were stored.

ALTER TABLE purser.pending_topups
    ADD COLUMN IF NOT EXISTS original_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS original_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS eur_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.pending_topups
    DROP CONSTRAINT IF EXISTS chk_pending_topups_fx,
    ADD CONSTRAINT chk_pending_topups_fx CHECK (
        (original_amount_cents IS NULL AND original_currency IS NULL AND eur_amount_cents IS NULL
            AND fx_units_per_eur IS NULL AND fx_source IS NULL AND fx_reference_date IS NULL)
        OR (original_amount_cents IS NOT NULL AND original_currency IS NOT NULL AND eur_amount_cents IS NOT NULL
            AND fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0
            AND fx_source IS NOT NULL AND fx_reference_date IS NOT NULL
            AND ((fx_source = 'identity' AND original_currency = 'EUR' AND fx_units_per_eur = 1
                    AND eur_amount_cents = original_amount_cents)
                 OR (fx_source = 'ecb' AND original_currency IN ('USD', 'GBP'))
                 OR (fx_source = 'legacy_quote' AND original_currency = 'USD')))
    ) NOT VALID;

ALTER TABLE purser.crypto_wallets
    ADD COLUMN IF NOT EXISTS original_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS original_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS eur_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS chk_crypto_wallets_fx,
    ADD CONSTRAINT chk_crypto_wallets_fx CHECK (
        (original_amount_cents IS NULL AND original_currency IS NULL AND eur_amount_cents IS NULL
            AND fx_units_per_eur IS NULL AND fx_source IS NULL AND fx_reference_date IS NULL)
        OR (original_amount_cents IS NOT NULL AND original_currency IS NOT NULL AND eur_amount_cents IS NOT NULL
            AND fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0
            AND fx_source IS NOT NULL AND fx_reference_date IS NOT NULL
            AND ((fx_source = 'identity' AND original_currency = 'EUR' AND fx_units_per_eur = 1
                    AND eur_amount_cents = original_amount_cents)
                 OR (fx_source = 'ecb' AND original_currency IN ('USD', 'GBP'))
                 OR (fx_source = 'legacy_quote' AND original_currency = 'USD')))
    ) NOT VALID;

ALTER TABLE purser.x402_payment_quotes
    ADD COLUMN IF NOT EXISTS original_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS original_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS eur_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.x402_payment_quotes
    DROP CONSTRAINT IF EXISTS chk_x402_payment_quotes_fx,
    ADD CONSTRAINT chk_x402_payment_quotes_fx CHECK (
        (original_amount_cents IS NULL AND original_currency IS NULL AND eur_amount_cents IS NULL
            AND fx_units_per_eur IS NULL AND fx_source IS NULL AND fx_reference_date IS NULL)
        OR (original_amount_cents IS NOT NULL AND original_currency IS NOT NULL AND eur_amount_cents IS NOT NULL
            AND fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0
            AND fx_source IS NOT NULL AND fx_reference_date IS NOT NULL
            AND ((fx_source = 'identity' AND original_currency = 'EUR' AND fx_units_per_eur = 1
                    AND eur_amount_cents = original_amount_cents)
                 OR (fx_source = 'ecb' AND original_currency IN ('USD', 'GBP'))
                 OR (fx_source = 'legacy_quote' AND original_currency = 'USD')))
    ) NOT VALID;

ALTER TABLE purser.payment_reversals
    ADD COLUMN IF NOT EXISTS original_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS original_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS eur_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.payment_reversals
    DROP CONSTRAINT IF EXISTS chk_payment_reversals_fx,
    ADD CONSTRAINT chk_payment_reversals_fx CHECK (
        (original_amount_cents IS NULL AND original_currency IS NULL AND eur_amount_cents IS NULL
            AND fx_units_per_eur IS NULL AND fx_source IS NULL AND fx_reference_date IS NULL)
        OR (original_amount_cents IS NOT NULL AND original_currency IS NOT NULL AND eur_amount_cents IS NOT NULL
            AND fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0
            AND fx_source IS NOT NULL AND fx_reference_date IS NOT NULL
            AND ((fx_source = 'identity' AND original_currency = 'EUR' AND fx_units_per_eur = 1
                    AND eur_amount_cents = original_amount_cents)
                 OR (fx_source = 'ecb' AND original_currency IN ('USD', 'GBP'))
                 OR (fx_source = 'legacy_quote' AND original_currency = 'USD')))
    ) NOT VALID;

ALTER TABLE purser.billing_payments
    ADD COLUMN IF NOT EXISTS original_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS original_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS eur_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.billing_payments
    DROP CONSTRAINT IF EXISTS chk_billing_payments_fx,
    ADD CONSTRAINT chk_billing_payments_fx CHECK (
        (original_amount_cents IS NULL AND original_currency IS NULL AND eur_amount_cents IS NULL
            AND fx_units_per_eur IS NULL AND fx_source IS NULL AND fx_reference_date IS NULL)
        OR (original_amount_cents IS NOT NULL AND original_currency IS NOT NULL AND eur_amount_cents IS NOT NULL
            AND fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0
            AND fx_source IS NOT NULL AND fx_reference_date IS NOT NULL
            AND ((fx_source = 'identity' AND original_currency = 'EUR' AND fx_units_per_eur = 1
                    AND eur_amount_cents = original_amount_cents)
                 OR (fx_source = 'ecb' AND original_currency IN ('USD', 'GBP'))
                 OR (fx_source = 'legacy_quote' AND original_currency = 'USD')))
    ) NOT VALID;

-- Invoices are computed in EUR and presented in the tenant's presentment
-- currency at the ECB rate of the finalization date.
ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS presentment_amount_cents BIGINT,
    ADD COLUMN IF NOT EXISTS presentment_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS presentment_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS presentment_reference_date DATE,
    ADD COLUMN IF NOT EXISTS finalized_at TIMESTAMPTZ;
ALTER TABLE purser.billing_invoices
    DROP CONSTRAINT IF EXISTS chk_billing_invoices_presentment,
    ADD CONSTRAINT chk_billing_invoices_presentment CHECK (
        (presentment_amount_cents IS NULL AND presentment_currency IS NULL
            AND presentment_units_per_eur IS NULL AND presentment_reference_date IS NULL)
        OR (presentment_amount_cents IS NOT NULL AND presentment_currency IS NOT NULL
            AND presentment_units_per_eur IS NOT NULL AND presentment_units_per_eur > 0
            AND presentment_reference_date IS NOT NULL
            AND ((presentment_currency = 'EUR' AND presentment_units_per_eur = 1)
                 OR presentment_currency IN ('USD', 'GBP')))
    ) NOT VALID;

-- Tax documents state net and VAT in EUR whatever currency they were paid in.
ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
    ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT;
ALTER TABLE purser.simplified_invoices
    DROP CONSTRAINT IF EXISTS chk_simplified_invoices_eur_amounts,
    ADD CONSTRAINT chk_simplified_invoices_eur_amounts CHECK (
        (net_eur_cents IS NULL AND vat_eur_cents IS NULL)
        OR (net_eur_cents IS NOT NULL AND vat_eur_cents IS NOT NULL
            AND net_eur_cents >= 0 AND vat_eur_cents >= 0)
    ) NOT VALID;

ALTER TABLE purser.crypto_invoices
    ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
    ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT;
ALTER TABLE purser.crypto_invoices
    DROP CONSTRAINT IF EXISTS chk_crypto_invoices_eur_amounts,
    ADD CONSTRAINT chk_crypto_invoices_eur_amounts CHECK (
        (net_eur_cents IS NULL AND vat_eur_cents IS NULL)
        OR (net_eur_cents IS NOT NULL AND vat_eur_cents IS NOT NULL
            AND net_eur_cents >= 0 AND vat_eur_cents >= 0)
    ) NOT VALID;
