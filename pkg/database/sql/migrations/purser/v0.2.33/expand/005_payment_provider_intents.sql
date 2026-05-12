-- Canonical pre-provider-call intent for every external payment side effect:
-- Stripe checkout sessions, Stripe overage PaymentIntents, Mollie first
-- payments, Mollie subscription creations, and Mollie/Stripe top-ups. The
-- row is inserted and committed before the provider API call so a crash or
-- timeout never leaves an orphan provider object without a local audit
-- trail. idempotency_key is deterministic per (purpose, local_reference)
-- so retries collapse to the same row.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE TABLE IF NOT EXISTS purser.payment_provider_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    provider VARCHAR(20) NOT NULL,
    purpose VARCHAR(40) NOT NULL,
    local_reference_type VARCHAR(40),
    local_reference_id UUID,
    provider_customer_id VARCHAR(255),
    provider_session_id VARCHAR(255),
    provider_subscription_id VARCHAR(255),
    provider_payment_id VARCHAR(255),
    status VARCHAR(40) NOT NULL DEFAULT 'pending',
    currency CHAR(3) NOT NULL,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    idempotency_key VARCHAR(128) NOT NULL,
    last_error TEXT,
    attempt_count INT NOT NULL DEFAULT 0,
    succeeded_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_payment_intent_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_payment_intent_purpose CHECK (purpose IN (
        'tenant_subscription_checkout',
        'cluster_subscription_checkout',
        'mollie_first_payment',
        'mollie_subscription_create',
        'stripe_overage_charge',
        'mollie_overage_charge',
        'prepaid_topup'
    )),
    CONSTRAINT chk_payment_intent_status CHECK (status IN (
        'pending', 'provider_open', 'sca_required',
        'succeeded', 'expired', 'cancelled',
        'provider_call_failed', 'terminal_failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_idem
    ON purser.payment_provider_intents(provider, idempotency_key);

CREATE INDEX IF NOT EXISTS idx_payment_provider_intents_tenant
    ON purser.payment_provider_intents(tenant_id, purpose, status);

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_session
    ON purser.payment_provider_intents(provider, provider_session_id)
    WHERE provider_session_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_payment
    ON purser.payment_provider_intents(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_subscription
    ON purser.payment_provider_intents(provider, provider_subscription_id)
    WHERE provider_subscription_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_payment_provider_intents_local_ref
    ON purser.payment_provider_intents(local_reference_type, local_reference_id)
    WHERE local_reference_id IS NOT NULL;
