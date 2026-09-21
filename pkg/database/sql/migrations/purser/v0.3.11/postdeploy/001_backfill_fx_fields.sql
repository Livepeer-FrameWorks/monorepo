-- EUR amounts carry identity FX. Non-EUR balance movements are converted by
-- the purser_eur_ledger_conversion_v0_3_11 data migration, which also fills the
-- FX fields of the rows it converts.

UPDATE purser.pending_topups
SET original_amount_cents = amount_cents,
    original_currency = 'EUR',
    eur_amount_cents = amount_cents,
    fx_units_per_eur = 1,
    fx_source = 'identity',
    fx_reference_date = (COALESCE(created_at, NOW()) AT TIME ZONE 'UTC')::date
WHERE fx_source IS NULL
  AND UPPER(currency) = 'EUR';

-- A prepaid crypto wallet is EUR-denominated when it was credited in EUR or,
-- before crediting, carries the USD-to-EUR quote that EUR wallets require.
-- The legacy rate columns read here and below are dropped by the contract
-- phase, so each read runs only while its column exists.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'crypto_wallets'
          AND column_name = 'quoted_usd_to_eur_rate'
    ) THEN
        EXECUTE $sql$
            UPDATE purser.crypto_wallets
            SET original_amount_cents = COALESCE(credited_amount_cents, expected_amount_cents),
                original_currency = 'EUR',
                eur_amount_cents = COALESCE(credited_amount_cents, expected_amount_cents),
                fx_units_per_eur = 1,
                fx_source = 'identity',
                fx_reference_date = (COALESCE(quoted_at, created_at, NOW()) AT TIME ZONE 'UTC')::date
            WHERE fx_source IS NULL
              AND purpose = 'prepaid'
              AND expected_amount_cents IS NOT NULL
              AND (credited_amount_currency = 'EUR'
                   OR (credited_amount_currency IS NULL AND quoted_usd_to_eur_rate IS NOT NULL))
        $sql$;
    END IF;
END $$;

-- x402 quotes are paid in USDC (six decimals) and credited in EUR at the
-- quoted EUR-per-USD rate.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'x402_payment_quotes'
          AND column_name = 'eur_per_usd_rate'
    ) THEN
        EXECUTE $sql$
            UPDATE purser.x402_payment_quotes
            SET original_amount_cents = ROUND(amount_atomic / 10000)::bigint,
                original_currency = 'USD',
                eur_amount_cents = credit_amount_cents,
                fx_units_per_eur = ROUND(1 / eur_per_usd_rate, 10),
                fx_source = 'legacy_quote',
                fx_reference_date = (created_at AT TIME ZONE 'UTC')::date
            WHERE fx_source IS NULL
              AND credit_currency = 'EUR'
        $sql$;
    END IF;
END $$;

UPDATE purser.payment_reversals
SET original_amount_cents = amount_cents,
    original_currency = 'EUR',
    eur_amount_cents = amount_cents,
    fx_units_per_eur = 1,
    fx_source = 'identity',
    fx_reference_date = (created_at AT TIME ZONE 'UTC')::date
WHERE fx_source IS NULL
  AND UPPER(currency) = 'EUR';

UPDATE purser.billing_payments
SET original_amount_cents = ROUND(amount * 100)::bigint,
    original_currency = 'EUR',
    eur_amount_cents = ROUND(amount * 100)::bigint,
    fx_units_per_eur = 1,
    fx_source = 'identity',
    fx_reference_date = (COALESCE(confirmed_at, created_at, NOW()) AT TIME ZONE 'UTC')::date
WHERE fx_source IS NULL
  AND UPPER(currency) = 'EUR';

-- Finalized EUR invoices were presented in EUR.
UPDATE purser.billing_invoices
SET presentment_amount_cents = ROUND(amount * 100)::bigint,
    presentment_currency = 'EUR',
    presentment_units_per_eur = 1,
    presentment_reference_date = (COALESCE(created_at, NOW()) AT TIME ZONE 'UTC')::date,
    finalized_at = COALESCE(finalized_at, created_at, NOW())
WHERE presentment_currency IS NULL
  AND status NOT IN ('draft', 'manual_review')
  AND UPPER(currency) = 'EUR';

UPDATE purser.simplified_invoices
SET net_eur_cents = net_amount_cents,
    vat_eur_cents = vat_amount_cents
WHERE net_eur_cents IS NULL
  AND UPPER(currency) = 'EUR';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'simplified_invoices'
          AND column_name = 'ecb_rate'
    ) THEN
        EXECUTE $sql$
            UPDATE purser.simplified_invoices
            SET net_eur_cents = ROUND(net_amount_cents * ecb_rate)::bigint,
                vat_eur_cents = ROUND(vat_amount_cents * ecb_rate)::bigint
            WHERE net_eur_cents IS NULL
              AND UPPER(currency) <> 'EUR'
              AND ecb_rate IS NOT NULL
        $sql$;
    END IF;
END $$;

UPDATE purser.crypto_invoices
SET net_eur_cents = net_amount_cents,
    vat_eur_cents = vat_amount_cents
WHERE net_eur_cents IS NULL
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
            SET net_eur_cents = ROUND(net_amount_cents * ecb_rate)::bigint,
                vat_eur_cents = ROUND(vat_amount_cents * ecb_rate)::bigint
            WHERE net_eur_cents IS NULL
              AND UPPER(currency) <> 'EUR'
              AND ecb_rate IS NOT NULL
        $sql$;
    END IF;
END $$;
