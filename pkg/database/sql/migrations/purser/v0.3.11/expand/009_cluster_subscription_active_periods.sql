-- Closed active spans remain billable after the current subscription is reactivated.
CREATE TABLE IF NOT EXISTS purser.cluster_subscription_active_periods (
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    active_from TIMESTAMPTZ NOT NULL,
    active_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, cluster_id, active_from),
    CHECK (active_until >= active_from)
);

CREATE OR REPLACE FUNCTION purser.preserve_cluster_subscription_active_period()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.stripe_subscription_id IS NULL AND OLD.status = 'cancelled'
       AND NEW.status = 'active' AND OLD.cancelled_at IS NOT NULL THEN
        INSERT INTO purser.cluster_subscription_active_periods (tenant_id, cluster_id, active_from, active_until)
        VALUES (OLD.tenant_id, OLD.cluster_id, COALESCE(OLD.activated_at, OLD.created_at), OLD.cancelled_at)
        ON CONFLICT (tenant_id, cluster_id, active_from) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_cluster_subscription_active_period ON purser.cluster_subscriptions;
CREATE TRIGGER trg_cluster_subscription_active_period
    BEFORE UPDATE OF status ON purser.cluster_subscriptions
    FOR EACH ROW EXECUTE FUNCTION purser.preserve_cluster_subscription_active_period();
