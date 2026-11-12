ALTER TABLE purser.provider_settlements
    ADD COLUMN IF NOT EXISTS settled_eur_cents BIGINT,
    ADD COLUMN IF NOT EXISTS fee_eur_cents BIGINT,
    ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
    ADD COLUMN IF NOT EXISTS valuation_units_per_eur NUMERIC(20,10),
    ADD COLUMN IF NOT EXISTS valuation_reference_date DATE,
    ADD COLUMN IF NOT EXISTS valuation_source VARCHAR(16),
    ADD COLUMN IF NOT EXISTS last_error_code VARCHAR(64),
    ADD COLUMN IF NOT EXISTS last_error TEXT;


-- An EUR provider settlement already is its EUR valuation. Populate the full
-- identity record atomically for writers that supply only native EUR fields.
CREATE OR REPLACE FUNCTION purser.ensure_provider_settlement_identity_valuation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'settled' AND NEW.settlement_currency = 'EUR'
       AND NEW.settled_eur_cents IS NULL AND NEW.fee_eur_cents IS NULL AND NEW.net_eur_cents IS NULL
       AND NEW.valuation_units_per_eur IS NULL AND NEW.valuation_reference_date IS NULL AND NEW.valuation_source IS NULL THEN
        NEW.settled_eur_cents := NEW.settled_amount_cents;
        NEW.fee_eur_cents := NEW.fee_cents;
        NEW.net_eur_cents := NEW.net_cents;
        NEW.valuation_units_per_eur := 1;
        NEW.valuation_reference_date := (NEW.settled_at AT TIME ZONE 'UTC')::date;
        NEW.valuation_source := 'identity';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_provider_settlement_identity_valuation ON purser.provider_settlements;
CREATE TRIGGER trg_provider_settlement_identity_valuation
BEFORE INSERT OR UPDATE ON purser.provider_settlements
FOR EACH ROW EXECUTE FUNCTION purser.ensure_provider_settlement_identity_valuation();

UPDATE purser.provider_settlements
SET settled_eur_cents = settled_amount_cents, fee_eur_cents = fee_cents, net_eur_cents = net_cents,
    valuation_units_per_eur = 1, valuation_reference_date = (settled_at AT TIME ZONE 'UTC')::date,
    valuation_source = 'identity'
WHERE status = 'settled' AND settlement_currency = 'EUR'
  AND settled_eur_cents IS NULL AND fee_eur_cents IS NULL AND net_eur_cents IS NULL
  AND valuation_units_per_eur IS NULL AND valuation_reference_date IS NULL AND valuation_source IS NULL;

ALTER TABLE purser.provider_settlements DROP CONSTRAINT IF EXISTS chk_provider_settlements_state;
ALTER TABLE purser.provider_settlements ADD
    CONSTRAINT chk_provider_settlements_state CHECK (
        status IN ('pending', 'recorded', 'blocked', 'settled')
        AND CASE WHEN status = 'pending' THEN
            provider_balance_transaction_id IS NULL AND settled_amount_cents IS NULL
            AND fee_cents IS NULL AND net_cents IS NULL AND settlement_currency IS NULL
            AND exchange_rate IS NULL AND settled_at IS NULL
        ELSE
            provider_balance_transaction_id IS NOT NULL AND btrim(provider_balance_transaction_id) <> ''
            AND settled_amount_cents IS NOT NULL AND fee_cents IS NOT NULL AND fee_cents >= 0
            AND net_cents IS NOT NULL AND net_cents = settled_amount_cents - fee_cents
            AND settlement_currency IS NOT NULL AND settlement_currency ~ '^[A-Z]{3}$'
            AND settled_at IS NOT NULL
            AND ((exchange_rate IS NOT NULL AND exchange_rate > 0)
                 OR (exchange_rate IS NULL AND charge_currency = settlement_currency))
        END
        AND CASE WHEN status = 'settled' THEN
            settled_eur_cents IS NOT NULL AND fee_eur_cents IS NOT NULL AND fee_eur_cents >= 0
            AND net_eur_cents IS NOT NULL AND net_eur_cents = settled_eur_cents - fee_eur_cents
            AND valuation_units_per_eur IS NOT NULL AND valuation_units_per_eur > 0
            AND valuation_reference_date IS NOT NULL AND valuation_source IS NOT NULL
            AND valuation_reference_date <= (settled_at AT TIME ZONE 'UTC')::date
            AND valuation_reference_date >= (settled_at AT TIME ZONE 'UTC')::date - 5
            AND ((settlement_currency = 'EUR' AND valuation_source = 'identity'
                  AND valuation_units_per_eur = 1 AND settled_eur_cents = settled_amount_cents
                  AND fee_eur_cents = fee_cents AND net_eur_cents = net_cents)
                 OR (settlement_currency IN ('USD', 'GBP') AND valuation_source = 'ecb'))
            AND last_error_code IS NULL AND last_error IS NULL
        ELSE
            settled_eur_cents IS NULL AND fee_eur_cents IS NULL AND net_eur_cents IS NULL
            AND valuation_units_per_eur IS NULL AND valuation_reference_date IS NULL AND valuation_source IS NULL
        END
        AND (status <> 'blocked' OR COALESCE(last_error_code, '') <> '')
    ) NOT VALID;
