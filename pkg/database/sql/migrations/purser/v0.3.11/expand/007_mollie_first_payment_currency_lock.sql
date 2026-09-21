-- An open Mollie first payment or Stripe subscription checkout fixes the
-- tenant's currency until completion or expiry. Provider checkout amounts and
-- webhook activation use the currency recorded on the intent. The one-day
-- bound releases abandoned intents even if their expiry webhook never arrives.

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
    ) OR EXISTS (
        SELECT 1
        FROM purser.payment_provider_intents
        WHERE tenant_id = p_tenant_id
          AND ((provider = 'mollie' AND purpose = 'mollie_first_payment')
               OR (provider = 'stripe' AND purpose IN ('tenant_subscription_checkout', 'cluster_subscription_checkout')))
          AND status IN ('pending', 'provider_open')
          AND updated_at > NOW() - INTERVAL '1 day'
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
        RAISE EXCEPTION 'PRESENTMENT_CURRENCY_LOCKED: tenant % has a provider subscription, an open invoice, or a pending first payment', OLD.tenant_id;
    END IF;
    RETURN NEW;
END;
$$;
