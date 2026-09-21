-- EUR tax documents carry the identity rate on their issue date. Documents in
-- another currency carry the EUR-per-USD rate they were issued with, stored
-- as units of that currency per euro.
UPDATE purser.simplified_invoices
SET fx_units_per_eur = 1,
    fx_reference_date = (issued_at AT TIME ZONE 'UTC')::date
WHERE fx_units_per_eur IS NULL
  AND UPPER(currency) = 'EUR';

-- ecb_rate is dropped by the contract phase, so it is read only while it exists.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'simplified_invoices'
          AND column_name = 'ecb_rate'
    ) THEN
        EXECUTE $sql$
            UPDATE purser.simplified_invoices
            SET fx_units_per_eur = ROUND(1 / ecb_rate, 10),
                fx_reference_date = (COALESCE(fx_rate_observed_at, issued_at) AT TIME ZONE 'UTC')::date
            WHERE fx_units_per_eur IS NULL
              AND UPPER(currency) <> 'EUR'
              AND ecb_rate > 0
        $sql$;
    END IF;
END $$;

UPDATE purser.crypto_invoices
SET fx_units_per_eur = 1,
    fx_reference_date = (issued_at AT TIME ZONE 'UTC')::date
WHERE fx_units_per_eur IS NULL
  AND UPPER(currency) = 'EUR';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'crypto_invoices'
          AND column_name = 'ecb_rate'
    ) THEN
        EXECUTE $sql$
            UPDATE purser.crypto_invoices
            SET fx_units_per_eur = ROUND(1 / ecb_rate, 10),
                fx_reference_date = (COALESCE(fx_rate_observed_at, issued_at) AT TIME ZONE 'UTC')::date
            WHERE fx_units_per_eur IS NULL
              AND UPPER(currency) <> 'EUR'
              AND ecb_rate > 0
        $sql$;
    END IF;
END $$;

ALTER TABLE purser.simplified_invoices
    VALIDATE CONSTRAINT chk_simplified_invoices_fx;
ALTER TABLE purser.crypto_invoices
    VALIDATE CONSTRAINT chk_crypto_invoices_fx;
