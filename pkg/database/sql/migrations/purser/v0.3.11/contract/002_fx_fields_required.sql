-- Every writer records FX fields, the postdeploy backfill covered EUR rows, and
-- the purser_eur_ledger_conversion_v0_3_11 data migration covered the rest.
ALTER TABLE purser.pending_topups
    ALTER COLUMN original_amount_cents SET NOT NULL,
    ALTER COLUMN original_currency SET NOT NULL,
    ALTER COLUMN eur_amount_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_source SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

ALTER TABLE purser.x402_payment_quotes
    ALTER COLUMN original_amount_cents SET NOT NULL,
    ALTER COLUMN original_currency SET NOT NULL,
    ALTER COLUMN eur_amount_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_source SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

ALTER TABLE purser.payment_reversals
    ALTER COLUMN original_amount_cents SET NOT NULL,
    ALTER COLUMN original_currency SET NOT NULL,
    ALTER COLUMN eur_amount_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_source SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

ALTER TABLE purser.billing_payments
    ALTER COLUMN original_amount_cents SET NOT NULL,
    ALTER COLUMN original_currency SET NOT NULL,
    ALTER COLUMN eur_amount_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_source SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

-- Invoice crypto wallets are priced by their invoice payment row; prepaid
-- wallets carry the FX fields of the credit they quote.
ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS chk_crypto_wallets_prepaid_fx,
    ADD CONSTRAINT chk_crypto_wallets_prepaid_fx CHECK (purpose <> 'prepaid' OR fx_source IS NOT NULL);

ALTER TABLE purser.simplified_invoices
    ALTER COLUMN net_eur_cents SET NOT NULL,
    ALTER COLUMN vat_eur_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

ALTER TABLE purser.crypto_invoices
    ALTER COLUMN net_eur_cents SET NOT NULL,
    ALTER COLUMN vat_eur_cents SET NOT NULL,
    ALTER COLUMN fx_units_per_eur SET NOT NULL,
    ALTER COLUMN fx_reference_date SET NOT NULL;

ALTER TABLE purser.crypto_wallets DROP COLUMN IF EXISTS quoted_usd_to_eur_rate;
ALTER TABLE purser.x402_payment_quotes DROP COLUMN IF EXISTS eur_per_usd_rate;
ALTER TABLE purser.simplified_invoices DROP COLUMN IF EXISTS ecb_rate;
ALTER TABLE purser.crypto_invoices DROP COLUMN IF EXISTS ecb_rate;
