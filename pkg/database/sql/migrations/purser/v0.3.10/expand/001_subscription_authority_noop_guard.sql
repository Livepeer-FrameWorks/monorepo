CREATE OR REPLACE FUNCTION purser.media_authority_subscription_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.tier_id IS NOT DISTINCT FROM NEW.tier_id
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.billing_model IS NOT DISTINCT FROM NEW.billing_model
       AND OLD.payment_method IS NOT DISTINCT FROM NEW.payment_method
       AND OLD.stripe_subscription_id IS NOT DISTINCT FROM NEW.stripe_subscription_id
       AND OLD.mollie_subscription_id IS NOT DISTINCT FROM NEW.mollie_subscription_id
       AND OLD.billing_period_start IS NOT DISTINCT FROM NEW.billing_period_start
       AND OLD.billing_period_end IS NOT DISTINCT FROM NEW.billing_period_end THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_authority_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
