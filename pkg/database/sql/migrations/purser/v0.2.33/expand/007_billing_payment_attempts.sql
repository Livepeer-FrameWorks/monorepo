-- Per-attempt audit trail for a billing_payment that goes through one or
-- more provider charge attempts (off-session retries, SCA challenge, mandate
-- failure → retry). idempotency_key is sent to the provider so a half-failed
-- attempt cannot double-charge. The retry job reads (next_retry_at, status)
-- to pick rows ready for the next attempt. Existing billing_payments gain
-- the small denormalizations needed by partial-payment-aware settlement:
-- reversed_amount_cents lets the paid-check subtract reversals without a
-- join, and intent_id ties the payment row to its canonical intent.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE TABLE IF NOT EXISTS purser.billing_payment_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id UUID NOT NULL REFERENCES purser.billing_payments(id) ON DELETE CASCADE,
    intent_id UUID REFERENCES purser.payment_provider_intents(id),
    attempt_number INT NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    provider VARCHAR(20) NOT NULL,
    provider_payment_id VARCHAR(255),
    status VARCHAR(40) NOT NULL DEFAULT 'pending',
    failure_code VARCHAR(100),
    failure_message TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_billing_payment_attempt_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_billing_payment_attempt_status CHECK (status IN (
        'pending', 'provider_open', 'sca_required',
        'succeeded', 'failed', 'expired', 'cancelled',
        'provider_call_failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_payment_attempts_seq
    ON purser.billing_payment_attempts(payment_id, attempt_number);

CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_payment_attempts_idem
    ON purser.billing_payment_attempts(provider, idempotency_key);

CREATE INDEX IF NOT EXISTS idx_billing_payment_attempts_next_retry
    ON purser.billing_payment_attempts(next_retry_at, status)
    WHERE next_retry_at IS NOT NULL AND status = 'provider_call_failed';

CREATE INDEX IF NOT EXISTS idx_billing_payment_attempts_provider_id
    ON purser.billing_payment_attempts(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

ALTER TABLE purser.billing_payments
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id),
    ADD COLUMN IF NOT EXISTS reversed_amount_cents BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_billing_payments_intent
    ON purser.billing_payments(intent_id)
    WHERE intent_id IS NOT NULL;
