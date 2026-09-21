-- ECB euro foreign exchange reference rates. units_per_eur is the ECB quote:
-- units of the currency that one euro buys on reference_date.
CREATE TABLE IF NOT EXISTS purser.fx_rates (
    currency CHAR(3) NOT NULL,
    reference_date DATE NOT NULL,
    units_per_eur NUMERIC(20, 10) NOT NULL,
    source VARCHAR(16) NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (currency, reference_date),
    CONSTRAINT chk_fx_rates_currency CHECK (currency IN ('USD', 'GBP')),
    CONSTRAINT chk_fx_rates_units_per_eur CHECK (units_per_eur > 0),
    CONSTRAINT chk_fx_rates_source CHECK (source = 'ecb')
);

-- Existing subscriptions keep EUR, the currency they were billed in; the
-- postdeploy phase derives the presentment currency of unlocked tenants from
-- their billing country. New subscriptions without a billing country present
-- in USD.
ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS presentment_currency CHAR(3) NOT NULL DEFAULT 'EUR';
ALTER TABLE purser.tenant_subscriptions
    ALTER COLUMN presentment_currency SET DEFAULT 'USD';
ALTER TABLE purser.tenant_subscriptions
    DROP CONSTRAINT IF EXISTS chk_tenant_subscriptions_presentment_currency,
    ADD CONSTRAINT chk_tenant_subscriptions_presentment_currency
    CHECK (presentment_currency IN ('EUR', 'USD', 'GBP')) NOT VALID;

-- A tenant's presentment currency is fixed while a provider subscription or a
-- finalized unpaid invoice exists, because Stripe customers and subscriptions
-- are single-currency and an open invoice was presented in the current one.
CREATE OR REPLACE FUNCTION purser.tenant_presentment_currency_locked(p_tenant_id UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM purser.tenant_subscriptions
        WHERE tenant_id = p_tenant_id
          AND (stripe_subscription_id IS NOT NULL OR mollie_subscription_id IS NOT NULL)
    ) OR EXISTS (
        SELECT 1
        FROM purser.cluster_subscriptions
        WHERE tenant_id = p_tenant_id
          AND stripe_subscription_id IS NOT NULL
          AND status <> 'cancelled'
    ) OR EXISTS (
        SELECT 1
        FROM purser.billing_invoices
        WHERE tenant_id = p_tenant_id
          AND status IN ('pending', 'overdue', 'failed')
    );
$$;

CREATE OR REPLACE FUNCTION purser.guard_presentment_currency_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.presentment_currency IS DISTINCT FROM OLD.presentment_currency
       AND (OLD.stripe_subscription_id IS NOT NULL
            OR OLD.mollie_subscription_id IS NOT NULL
            OR NEW.stripe_subscription_id IS NOT NULL
            OR NEW.mollie_subscription_id IS NOT NULL
            OR purser.tenant_presentment_currency_locked(OLD.tenant_id)) THEN
        RAISE EXCEPTION 'PRESENTMENT_CURRENCY_LOCKED: tenant % has a provider subscription or an open invoice', OLD.tenant_id;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_tenant_subscriptions_presentment_currency_lock ON purser.tenant_subscriptions;
CREATE TRIGGER trg_tenant_subscriptions_presentment_currency_lock
    BEFORE UPDATE OF presentment_currency ON purser.tenant_subscriptions
    FOR EACH ROW EXECUTE FUNCTION purser.guard_presentment_currency_change();
