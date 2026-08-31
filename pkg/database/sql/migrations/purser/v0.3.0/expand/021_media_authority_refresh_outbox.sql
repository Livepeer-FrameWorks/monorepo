CREATE TABLE IF NOT EXISTS purser.media_authority_refresh_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_event_id VARCHAR(255) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    reason VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_purser_media_authority_refresh_status
        CHECK (status IN ('pending', 'delivering', 'completed')),
    CONSTRAINT chk_purser_media_authority_refresh_reason
        CHECK (btrim(reason) <> '')
);

CREATE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_pending
    ON purser.media_authority_refresh_outbox(next_attempt_at, created_at)
    WHERE status <> 'completed';

CREATE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_tenant
    ON purser.media_authority_refresh_outbox(tenant_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_coalesced
    ON purser.media_authority_refresh_outbox(tenant_id, reason)
    WHERE status <> 'completed';

CREATE OR REPLACE FUNCTION purser.enqueue_media_authority_refresh(
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    VALUES (p_reason || ':' || gen_random_uuid()::text, p_tenant_id, p_reason)
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending',
        next_attempt_at = NOW(),
        completed_at = NULL,
        last_error = NULL,
        -- Keep an active delivery lease as a short serialization fence. The
        -- claimed revision can no longer complete this row, and the replacement
        -- revision becomes claimable as soon as that lease ends.
        lease_expires_at = CASE
            WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at
            ELSE NULL
        END,
        updated_at = NOW();
END;
$$;

CREATE OR REPLACE FUNCTION purser.media_authority_subscription_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_authority_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_tenant_subscription_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tier_id, status, billing_model, payment_method,
    stripe_subscription_id, mollie_subscription_id, billing_period_start, billing_period_end
ON purser.tenant_subscriptions
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_subscription_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_prepaid_gate_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    IF TG_OP = 'UPDATE' AND ((OLD.balance_cents <= 0) = (NEW.balance_cents <= 0)) THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'prepaid_admission_gate_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_prepaid_balance_media_authority
AFTER INSERT OR DELETE OR UPDATE OF balance_cents
ON purser.prepaid_balances
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_prepaid_gate_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_subscription_entitlement_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_subscription UUID;
    affected_tenant UUID;
BEGIN
    affected_subscription := CASE WHEN TG_OP = 'DELETE' THEN OLD.subscription_id ELSE NEW.subscription_id END;
    SELECT tenant_id INTO affected_tenant
    FROM purser.tenant_subscriptions
    WHERE id = affected_subscription;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_entitlement_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_subscription_entitlement_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.subscription_entitlement_overrides
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_subscription_entitlement_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_tier UUID;
    new_tier UUID;
    change_reason TEXT;
BEGIN
    old_tier := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN OLD.tier_id ELSE NULL END;
    new_tier := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN NEW.tier_id ELSE NULL END;
    change_reason := CASE TG_TABLE_NAME
        WHEN 'tier_entitlements' THEN 'tier_entitlement_changed'
        ELSE 'tier_allowance_changed'
    END;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    SELECT change_reason || ':' || gen_random_uuid()::text, affected.tenant_id, change_reason
    FROM (
        SELECT DISTINCT subscriptions.tenant_id
        FROM purser.tenant_subscriptions AS subscriptions
        WHERE subscriptions.tier_id IN (old_tier, new_tier)
          AND subscriptions.status <> 'cancelled'
    ) AS affected
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending', next_attempt_at = NOW(), completed_at = NULL,
        last_error = NULL,
        lease_expires_at = CASE WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at ELSE NULL END,
        updated_at = NOW();
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_tier_entitlement_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.tier_entitlements
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_tier_changed();

CREATE OR REPLACE TRIGGER trg_tier_pricing_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.tier_pricing_rules
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_tier_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_billing_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tier UUID;
BEGIN
    affected_tier := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    SELECT 'billing_tier_authority_changed:' || gen_random_uuid()::text,
           affected.tenant_id, 'billing_tier_authority_changed'
    FROM (
        SELECT DISTINCT subscriptions.tenant_id
        FROM purser.tenant_subscriptions AS subscriptions
        WHERE subscriptions.tier_id = affected_tier
          AND subscriptions.status <> 'cancelled'
    ) AS affected
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending', next_attempt_at = NOW(), completed_at = NULL,
        last_error = NULL,
        lease_expires_at = CASE WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at ELSE NULL END,
        updated_at = NOW();
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_billing_tier_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tier_name, tier_level, is_active, features,
    processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
ON purser.billing_tiers
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_billing_tier_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_usage_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
    affected_meter TEXT;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    affected_meter := CASE WHEN TG_OP = 'DELETE' THEN OLD.usage_type ELSE NEW.usage_type END;
    IF affected_meter = 'delivered_minutes' THEN
        PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'allowance_usage_changed');
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_usage_record_media_authority
AFTER INSERT OR DELETE OR UPDATE OF usage_type, usage_value, value_kind, granularity, period_start, period_end
ON purser.usage_records
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_usage_changed();

CREATE OR REPLACE TRIGGER trg_usage_adjustment_media_authority
AFTER INSERT OR DELETE OR UPDATE OF usage_type, delta_value, status, period_start, period_end
ON purser.usage_adjustments
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_usage_changed();
