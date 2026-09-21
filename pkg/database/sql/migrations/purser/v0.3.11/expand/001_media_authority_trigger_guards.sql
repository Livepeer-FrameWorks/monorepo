CREATE OR REPLACE FUNCTION purser.media_authority_subscription_entitlement_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_subscription UUID;
    affected_tenant UUID;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
    affected_subscription := CASE WHEN TG_OP = 'DELETE' THEN OLD.subscription_id ELSE NEW.subscription_id END;
    SELECT tenant_id INTO affected_tenant
    FROM purser.tenant_subscriptions
    WHERE id = affected_subscription;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_entitlement_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE FUNCTION purser.media_authority_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_tier UUID;
    new_tier UUID;
    change_reason TEXT;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
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

CREATE OR REPLACE FUNCTION purser.media_authority_billing_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tier UUID;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
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

CREATE OR REPLACE FUNCTION purser.media_authority_usage_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
    affected_meter TEXT;
BEGIN
    -- A redelivered usage report upserts the same values and changes no allowance.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    affected_meter := CASE WHEN TG_OP = 'DELETE' THEN OLD.usage_type ELSE NEW.usage_type END;
    IF affected_meter = 'delivered_minutes' THEN
        PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'allowance_usage_changed');
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
