ALTER TABLE purser.billing_tiers
    ADD COLUMN IF NOT EXISTS operator_grant_fallback_revision BIGINT NOT NULL DEFAULT 0;

ALTER TABLE purser.subscription_operator_grants
    ADD COLUMN IF NOT EXISTS fallback_tier_id UUID,
    ADD COLUMN IF NOT EXISTS assigned_tier_id UUID,
    ADD COLUMN IF NOT EXISTS expiry_attempted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS expiry_error TEXT,
    ADD COLUMN IF NOT EXISTS expiry_failures INTEGER NOT NULL DEFAULT 0;

ALTER TABLE purser.subscription_operator_grants
    DROP CONSTRAINT IF EXISTS subscription_operator_grants_fallback_tier_id_fkey;
ALTER TABLE purser.subscription_operator_grants
    ADD CONSTRAINT subscription_operator_grants_fallback_tier_id_fkey
    FOREIGN KEY (fallback_tier_id) REFERENCES purser.billing_tiers(id) NOT VALID;
ALTER TABLE purser.subscription_operator_grants
    DROP CONSTRAINT IF EXISTS subscription_operator_grants_assigned_tier_id_fkey;
ALTER TABLE purser.subscription_operator_grants
    ADD CONSTRAINT subscription_operator_grants_assigned_tier_id_fkey
    FOREIGN KEY (assigned_tier_id) REFERENCES purser.billing_tiers(id) NOT VALID;

-- Immutable pricing phases preserve the arrangement used to rate past usage.
CREATE TABLE IF NOT EXISTS purser.subscription_operator_grant_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    subscription_id UUID NOT NULL REFERENCES purser.tenant_subscriptions(id) ON DELETE CASCADE,
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id),
    fallback_tier_id UUID REFERENCES purser.billing_tiers(id),
    base_price NUMERIC(10,2),
    waive_usage BOOLEAN NOT NULL,
    collection VARCHAR(16) NOT NULL,
    expires_at TIMESTAMPTZ,
    reason TEXT NOT NULL,
    granted_by TEXT,
    granted_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_operator_grant_history_tenant_time
ON purser.subscription_operator_grant_history(tenant_id, granted_at, ended_at);

-- The transaction-local frameworks.operator_grant_boundary carries a late expiry
-- deadline to this trigger so historical prices end at the deadline, not sweep time.
CREATE OR REPLACE FUNCTION purser.archive_operator_grant()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO purser.subscription_operator_grant_history
        (tenant_id, subscription_id, tier_id, fallback_tier_id, base_price, waive_usage,
         collection, expires_at, reason, granted_by, granted_at, ended_at)
    SELECT ts.tenant_id, OLD.subscription_id, ts.tier_id,
           OLD.fallback_tier_id, OLD.base_price, OLD.waive_usage, OLD.collection,
           OLD.expires_at, OLD.reason, OLD.granted_by, OLD.granted_at,
           LEAST(COALESCE(OLD.expires_at, 'infinity'::timestamptz),
               CASE WHEN TG_OP = 'UPDATE' THEN NEW.granted_at
               ELSE COALESCE(NULLIF(current_setting('frameworks.operator_grant_boundary', true), '')::timestamptz, NOW()) END)
    FROM purser.tenant_subscriptions ts WHERE ts.id = OLD.subscription_id;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_operator_grant_history
BEFORE UPDATE OF base_price, waive_usage, collection, expires_at, reason, granted_by, granted_at, fallback_tier_id, assigned_tier_id OR DELETE ON purser.subscription_operator_grants
FOR EACH ROW EXECUTE FUNCTION purser.archive_operator_grant();

CREATE OR REPLACE FUNCTION purser.operator_grant_authority_changed()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected UUID;
BEGIN
    SELECT tenant_id INTO affected FROM purser.tenant_subscriptions
    WHERE id = CASE WHEN TG_OP = 'DELETE' THEN OLD.subscription_id ELSE NEW.subscription_id END;
    IF affected IS NOT NULL THEN
        PERFORM purser.enqueue_media_authority_refresh(affected, 'subscription_authority_changed');
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_operator_grant_authority
AFTER INSERT OR UPDATE OF base_price, waive_usage, collection, expires_at, reason, granted_by, granted_at, fallback_tier_id, assigned_tier_id OR DELETE ON purser.subscription_operator_grants
FOR EACH ROW EXECUTE FUNCTION purser.operator_grant_authority_changed();

CREATE INDEX IF NOT EXISTS idx_operator_grant_history_listing
ON purser.subscription_operator_grant_history(granted_at DESC, subscription_id, ended_at ASC);

CREATE INDEX IF NOT EXISTS idx_operator_grant_history_expiry
ON purser.subscription_operator_grant_history(expires_at ASC) WHERE expires_at IS NOT NULL;

-- Outstanding grants must retain an assignable recovery tier. Grant writers
-- hold a row lock on the fallback until their transaction commits.
-- Binding a fallback writes its parent row as well as locking it. Yugabyte's
-- Read Committed retirement statement must restart its snapshot after waiting
-- on a concurrent binding, so its guard sees the newly committed grant.
CREATE OR REPLACE FUNCTION purser.fence_operator_grant_fallback_binding()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.fallback_tier_id IS NOT NULL AND
       (TG_OP = 'INSERT' OR NEW.fallback_tier_id IS DISTINCT FROM OLD.fallback_tier_id) THEN
        UPDATE purser.billing_tiers SET operator_grant_fallback_revision = operator_grant_fallback_revision + 1
        WHERE id = NEW.fallback_tier_id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_fence_operator_grant_fallback_binding
BEFORE INSERT OR UPDATE OF fallback_tier_id ON purser.subscription_operator_grants
FOR EACH ROW EXECUTE FUNCTION purser.fence_operator_grant_fallback_binding();

CREATE OR REPLACE FUNCTION purser.guard_operator_grant_fallback_retirement()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF COALESCE(OLD.is_active, true) AND NOT COALESCE(NEW.is_active, true)
       AND EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row
                   WHERE grant_row.fallback_tier_id=OLD.id) THEN
        RAISE EXCEPTION 'billing tier is the fallback of an outstanding operator grant' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_guard_operator_grant_fallback_retirement
BEFORE UPDATE OF is_active ON purser.billing_tiers
FOR EACH ROW EXECUTE FUNCTION purser.guard_operator_grant_fallback_retirement();
