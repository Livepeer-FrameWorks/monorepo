-- A tenant without a provider subscription and a presentment currency other
-- than EUR pays its tier base fee in advance on a base-fee invoice. The invoice
-- carries the period the fee covers here instead of period_start, which stays
-- reserved for the period's usage invoice.
ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS base_fee_period_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS base_fee_period_end TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_invoices_base_fee_period
    ON purser.billing_invoices(tenant_id, base_fee_period_start)
    WHERE base_fee_period_start IS NOT NULL;

-- Tax documents record the rate and reference date of the amounts they state
-- in EUR next to the currency they were paid in.
ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.simplified_invoices
    DROP CONSTRAINT IF EXISTS chk_simplified_invoices_fx,
    ADD CONSTRAINT chk_simplified_invoices_fx CHECK (
        (fx_units_per_eur IS NULL AND fx_reference_date IS NULL)
        OR (fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0 AND fx_reference_date IS NOT NULL)
    ) NOT VALID;

ALTER TABLE purser.crypto_invoices
    ADD COLUMN IF NOT EXISTS fx_units_per_eur NUMERIC(20, 10),
    ADD COLUMN IF NOT EXISTS fx_reference_date DATE;
ALTER TABLE purser.crypto_invoices
    DROP CONSTRAINT IF EXISTS chk_crypto_invoices_fx,
    ADD CONSTRAINT chk_crypto_invoices_fx CHECK (
        (fx_units_per_eur IS NULL AND fx_reference_date IS NULL)
        OR (fx_units_per_eur IS NOT NULL AND fx_units_per_eur > 0 AND fx_reference_date IS NOT NULL)
    ) NOT VALID;

-- Quotes written by this release carry their rate in the FX fields; the
-- single-pair rate column is dropped in the contract phase. A schema that
-- already went through contract has no such column.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'x402_payment_quotes'
          AND column_name = 'eur_per_usd_rate'
    ) THEN
        ALTER TABLE purser.x402_payment_quotes ALTER COLUMN eur_per_usd_rate DROP NOT NULL;
    END IF;
END $$;
