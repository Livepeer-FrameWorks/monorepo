CREATE TABLE IF NOT EXISTS purser.automatic_topup_settings (
 tenant_id UUID PRIMARY KEY,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 threshold_eur_cents BIGINT NOT NULL CHECK(threshold_eur_cents BETWEEN 0 AND 100000),
 net_amount_minor BIGINT NOT NULL CHECK(net_amount_minor BETWEEN 500 AND 10000000),
 currency CHAR(3) NOT NULL CHECK(currency IN ('EUR','USD','GBP')),
 monthly_gross_limit_minor BIGINT NOT NULL CHECK(monthly_gross_limit_minor BETWEEN 500 AND 10000000),
 consent_version TEXT NOT NULL,
 consent_actor TEXT NOT NULL,
 consent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 revision UUID NOT NULL,
 stripe_customer_id TEXT NOT NULL,
 stripe_payment_method_id TEXT NOT NULL,
 profile_snapshot JSONB NOT NULL,
 paused_reason TEXT,
 last_attempt_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_automatic_topups_enabled ON purser.automatic_topup_settings(last_attempt_at ASC NULLS FIRST,tenant_id) WHERE enabled AND paused_reason IS NULL;
CREATE TABLE IF NOT EXISTS purser.automatic_topup_attempts (
 id UUID PRIMARY KEY,
 tenant_id UUID NOT NULL,
 settings_revision UUID NOT NULL,
 topup_id UUID NOT NULL REFERENCES purser.pending_topups(id),
 gross_amount_minor BIGINT NOT NULL CHECK(gross_amount_minor>0),
 currency CHAR(3) NOT NULL,
 budget_month DATE NOT NULL,
 customer_id TEXT NOT NULL,
 payment_method_id TEXT NOT NULL,
 profile_snapshot JSONB NOT NULL,
 consent_snapshot JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','submitted','settled','failed','cancelled','needs_review')),
 provider_payment_id TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,topup_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_automatic_topup_inflight ON purser.automatic_topup_attempts(tenant_id) WHERE status IN ('reserved','submitted','needs_review');
CREATE INDEX IF NOT EXISTS idx_automatic_topup_month ON purser.automatic_topup_attempts(tenant_id,budget_month,status);

CREATE OR REPLACE FUNCTION purser.retain_automatic_topup_attempt_facts()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.tenant_id,NEW.settings_revision,NEW.topup_id,NEW.gross_amount_minor,NEW.currency,NEW.budget_month,NEW.customer_id,NEW.payment_method_id,NEW.profile_snapshot,NEW.consent_snapshot)
 IS DISTINCT FROM ROW(OLD.id,OLD.tenant_id,OLD.settings_revision,OLD.topup_id,OLD.gross_amount_minor,OLD.currency,OLD.budget_month,OLD.customer_id,OLD.payment_method_id,OLD.profile_snapshot,OLD.consent_snapshot)
 THEN RAISE EXCEPTION 'automatic payment consent and reserved money are immutable'; END IF;
 IF OLD.provider_payment_id IS NOT NULL AND NEW.provider_payment_id IS DISTINCT FROM OLD.provider_payment_id THEN RAISE EXCEPTION 'automatic payment identity cannot change'; END IF;
 IF OLD.status='settled' AND NEW.status<>'settled' THEN RAISE EXCEPTION 'settled automatic funding cannot reopen'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER automatic_topup_attempt_facts_guard BEFORE UPDATE ON purser.automatic_topup_attempts FOR EACH ROW EXECUTE FUNCTION purser.retain_automatic_topup_attempt_facts();
