ALTER TABLE purser.prepaid_balances
 ADD COLUMN IF NOT EXISTS grace_started_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS grace_ends_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS grace_credit_cents BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS grace_recovered_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS grace_duration_seconds INTEGER NOT NULL DEFAULT 259200,
 ADD COLUMN IF NOT EXISTS grace_limit_cents BIGINT NOT NULL DEFAULT 1000,
 ADD COLUMN IF NOT EXISTS grace_rearm_seconds INTEGER NOT NULL DEFAULT 2592000,
 ADD COLUMN IF NOT EXISTS free_recovery_started_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS free_recovery_until TIMESTAMPTZ;
ALTER TABLE purser.prepaid_balances DROP CONSTRAINT IF EXISTS prepaid_grace_policy_valid;
ALTER TABLE purser.prepaid_balances ADD CONSTRAINT prepaid_grace_policy_valid CHECK (
 grace_duration_seconds >= 0 AND grace_duration_seconds <= 604800
 AND grace_limit_cents >= 0 AND grace_limit_cents <= 1000
 AND grace_rearm_seconds >= 2592000 AND grace_rearm_seconds <= 31536000 AND grace_credit_cents >= 0
 AND ((grace_started_at IS NULL AND grace_ends_at IS NULL) OR
      (grace_started_at IS NOT NULL AND grace_ends_at >= grace_started_at))) NOT VALID;

CREATE OR REPLACE FUNCTION purser.prepaid_grace_policy_value(p_tenant UUID,p_key TEXT,p_default BIGINT,p_min BIGINT,p_max BIGINT)
RETURNS BIGINT LANGUAGE sql STABLE AS $$
 SELECT COALESCE((SELECT CASE WHEN jsonb_typeof(o.value)='number' AND o.value::text ~ '^[0-9]{1,10}$'
       THEN CASE WHEN o.value::text::bigint BETWEEN p_min AND p_max THEN o.value::text::bigint ELSE 0 END ELSE 0 END
   FROM purser.subscription_entitlement_overrides o JOIN purser.tenant_subscriptions s ON s.id=o.subscription_id
   WHERE s.tenant_id=p_tenant AND o.key=p_key LIMIT 1),p_default);
$$;


ALTER TABLE purser.prepaid_balances
 ADD COLUMN IF NOT EXISTS low_notice_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS grace_notice_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS grace_reminder_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS recovery_notice_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS recovery_reminder_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS payment_notice_attempt_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS payment_notice_error TEXT;
CREATE INDEX IF NOT EXISTS idx_prepaid_payment_notice_attempt ON purser.prepaid_balances(payment_notice_attempt_at ASC NULLS FIRST,tenant_id);
CREATE OR REPLACE VIEW purser.prepaid_payment_notice_candidates AS
SELECT b.tenant_id,b.currency,b.balance_cents,b.grace_ends_at,b.grace_credit_cents,b.free_recovery_until,
       b.payment_notice_attempt_at,COALESCE(s.billing_email,'')::text AS recipient,COALESCE(s.billing_name,'')::text AS tenant_name,
 CASE
 WHEN t.tier_name='free' AND b.free_recovery_until IS NOT NULL AND b.recovery_notice_at IS NULL THEN 'recovery'
 WHEN t.tier_name='free' AND b.free_recovery_until <= NOW()+INTERVAL '7 days' AND b.recovery_reminder_at IS NULL THEN 'recovery_reminder'
 WHEN s.billing_model='prepaid' AND b.balance_cents<=0 AND b.balance_cents > -b.grace_credit_cents AND b.grace_ends_at>NOW() AND b.grace_notice_at IS NULL THEN 'grace'
 WHEN s.billing_model='prepaid' AND b.balance_cents<=0 AND b.balance_cents > -b.grace_credit_cents AND b.grace_ends_at BETWEEN NOW() AND NOW()+INTERVAL '24 hours' AND b.grace_reminder_at IS NULL THEN 'grace_reminder'
 WHEN s.billing_model='prepaid' AND b.balance_cents>0 AND b.balance_cents<=COALESCE(b.low_balance_threshold_cents,500) AND b.low_notice_at IS NULL THEN 'low'
 ELSE '' END::text AS kind
FROM purser.prepaid_balances b JOIN purser.tenant_subscriptions s ON s.tenant_id=b.tenant_id
JOIN purser.billing_tiers t ON t.id=s.tier_id
WHERE b.currency='EUR' AND s.status='active';


ALTER TABLE purser.prepaid_balances ADD COLUMN IF NOT EXISTS free_recovery_media_cutoff_at TIMESTAMPTZ;
-- Durable expiry decisions serialize with funding; an already-expired asset is not revived by a later deposit.
CREATE TABLE IF NOT EXISTS purser.media_recovery_expiries (
 tenant_id UUID NOT NULL,
 cutoff_at TIMESTAMPTZ NOT NULL,
 recovery_until TIMESTAMPTZ NOT NULL,
 committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,cutoff_at)
);
CREATE OR REPLACE FUNCTION purser.reject_media_recovery_expiry_mutation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'media recovery expiry receipts are append-only' USING ERRCODE='23514'; END; $$;
CREATE OR REPLACE TRIGGER media_recovery_expiries_append_only BEFORE UPDATE OR DELETE ON purser.media_recovery_expiries
 FOR EACH ROW EXECUTE FUNCTION purser.reject_media_recovery_expiry_mutation();

-- Snapshot one exhaustion episode under the same wallet lock as its debit.
-- Funding and reconnects cannot renew it; sustained solvency rearms grace.
CREATE OR REPLACE FUNCTION purser.track_prepaid_payment_grace()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE paid_credit BIGINT;
BEGIN
 IF NEW.currency <> 'EUR' THEN RETURN NEW; END IF;
 NEW.grace_duration_seconds := purser.prepaid_grace_policy_value(NEW.tenant_id,'billing_prepaid_grace_seconds',259200,0,604800);
 NEW.grace_limit_cents := purser.prepaid_grace_policy_value(NEW.tenant_id,'billing_prepaid_grace_credit_cents',1000,0,1000);
 NEW.grace_rearm_seconds := purser.prepaid_grace_policy_value(NEW.tenant_id,'billing_prepaid_grace_rearm_seconds',2592000,2592000,31536000);
 IF NEW.balance_cents > COALESCE(NEW.low_balance_threshold_cents,500) THEN NEW.low_notice_at:=NULL; END IF;
 IF NEW.balance_cents > 0 THEN
  IF OLD.balance_cents <= 0 THEN NEW.grace_recovered_at := NOW(); END IF;
  RETURN NEW;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions s JOIN purser.billing_tiers t ON t.id=s.tier_id
                WHERE s.tenant_id=NEW.tenant_id AND s.billing_model='prepaid' AND s.status='active'
                  AND t.is_default_prepaid AND t.is_active AND t.base_price=0) THEN RETURN NEW; END IF;
 IF NEW.grace_started_at IS NULL OR
    (OLD.grace_recovered_at IS NOT NULL AND OLD.grace_recovered_at <= NOW()-NEW.grace_rearm_seconds*INTERVAL '1 second') THEN
  SELECT COALESCE(MAX(amount_cents),0) INTO paid_credit FROM purser.balance_transactions
   WHERE tenant_id=NEW.tenant_id AND transaction_type='topup' AND amount_cents>0;
  IF paid_credit>0 AND NEW.grace_duration_seconds>0 AND NEW.grace_limit_cents>0 THEN
   NEW.grace_notice_at := NULL; NEW.grace_reminder_at := NULL;
   NEW.recovery_notice_at := NULL; NEW.recovery_reminder_at := NULL;
   NEW.payment_notice_attempt_at := NULL; NEW.payment_notice_error := NULL;
   NEW.grace_started_at := NOW();
   NEW.grace_ends_at := NOW()+NEW.grace_duration_seconds*INTERVAL '1 second';
   NEW.grace_credit_cents := LEAST(NEW.grace_limit_cents,paid_credit);
   NEW.free_recovery_media_cutoff_at := NULL;
   NEW.free_recovery_started_at := NULL;
   NEW.free_recovery_until := NULL;
  END IF;
 END IF;
 NEW.grace_recovered_at := NULL;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_prepaid_payment_grace BEFORE UPDATE OF balance_cents ON purser.prepaid_balances
 FOR EACH ROW EXECUTE FUNCTION purser.track_prepaid_payment_grace();

CREATE OR REPLACE FUNCTION purser.refresh_prepaid_grace_authority()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.grace_ends_at IS DISTINCT FROM OLD.grace_ends_at OR
    (OLD.balance_cents > -OLD.grace_credit_cents) IS DISTINCT FROM (NEW.balance_cents > -NEW.grace_credit_cents) THEN
  PERFORM purser.enqueue_media_authority_refresh(NEW.tenant_id,'prepaid_payment_grace_changed');
 END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_prepaid_grace_authority AFTER UPDATE OF balance_cents ON purser.prepaid_balances
 FOR EACH ROW EXECUTE FUNCTION purser.refresh_prepaid_grace_authority();


CREATE OR REPLACE FUNCTION purser.begin_free_media_recovery_on_tier_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.tier_id IS DISTINCT FROM NEW.tier_id AND EXISTS(SELECT 1 FROM purser.billing_tiers WHERE id=NEW.tier_id AND tier_name='free') THEN
  UPDATE purser.prepaid_balances
  SET free_recovery_media_cutoff_at=NOW(),
      free_recovery_started_at=COALESCE(free_recovery_started_at,NOW()),
      free_recovery_until=COALESCE(free_recovery_until,NOW()+INTERVAL '30 days')
  WHERE tenant_id=NEW.tenant_id AND currency='EUR';
 END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER trg_free_media_recovery_tier_change AFTER UPDATE OF tier_id ON purser.tenant_subscriptions
 FOR EACH ROW EXECUTE FUNCTION purser.begin_free_media_recovery_on_tier_change();
