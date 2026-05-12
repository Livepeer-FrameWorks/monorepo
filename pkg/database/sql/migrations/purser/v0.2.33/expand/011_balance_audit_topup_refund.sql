-- Manual operator interventions must capture who did it and why.
-- actor_kind is 'user' for ops dashboards, 'system' for automated
-- reconciliation writes, 'webhook' for provider-triggered rows. Prepaid
-- top-ups also gain provider payment/refund ids so the reversal path can
-- find the original charge from a webhook refund/chargeback event.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.balance_transactions
    ADD COLUMN IF NOT EXISTS actor_id UUID,
    ADD COLUMN IF NOT EXISTS actor_kind VARCHAR(20),
    ADD COLUMN IF NOT EXISTS reason TEXT,
    ADD COLUMN IF NOT EXISTS evidence_ref TEXT,
    ADD COLUMN IF NOT EXISTS reverses_transaction_id UUID REFERENCES purser.balance_transactions(id);

ALTER TABLE purser.balance_transactions
    ADD CONSTRAINT chk_balance_transactions_actor_kind CHECK (
        actor_kind IS NULL OR actor_kind IN ('user', 'system', 'webhook', 'job')
    ) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_balance_transactions_reverses
    ON purser.balance_transactions(reverses_transaction_id)
    WHERE reverses_transaction_id IS NOT NULL;

ALTER TABLE purser.pending_topups
    ADD COLUMN IF NOT EXISTS provider_payment_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS provider_charge_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS refunded_amount_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id);

ALTER TABLE purser.pending_topups
    ALTER COLUMN checkout_id DROP NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_pending_topups_provider_payment
    ON purser.pending_topups(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_pending_topups_intent
    ON purser.pending_topups(intent_id)
    WHERE intent_id IS NOT NULL;
