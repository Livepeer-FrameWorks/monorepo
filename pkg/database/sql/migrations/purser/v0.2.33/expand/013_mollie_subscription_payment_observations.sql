-- Out-of-order Mollie subscription payment webhooks land here when no
-- local invoice exists yet. The drain trigger runs at invoice finalization
-- to attach observations to the matching invoice; a periodic backstop
-- retries unresolved rows. The mandate state table is also normalised so
-- failed/expired first payments and revoked mandates have a durable home
-- instead of being represented by absence in mollie_mandates.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE TABLE IF NOT EXISTS purser.mollie_payment_observations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    mollie_payment_id VARCHAR(50) NOT NULL,
    mollie_subscription_id VARCHAR(50),
    mollie_mandate_id VARCHAR(50),
    sequence_type VARCHAR(20),
    status VARCHAR(20) NOT NULL,
    amount_cents BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    paid_at TIMESTAMPTZ,
    invoice_id UUID REFERENCES purser.billing_invoices(id),
    payment_id UUID REFERENCES purser.billing_payments(id),
    resolved_at TIMESTAMPTZ,
    resolution VARCHAR(40),
    attempt_count INT NOT NULL DEFAULT 0,
    last_error TEXT,
    raw_payload BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_mollie_obs_status CHECK (status IN (
        'open', 'pending', 'paid', 'failed', 'expired', 'cancelled', 'authorized'
    )),
    CONSTRAINT chk_mollie_obs_resolution CHECK (resolution IS NULL OR resolution IN (
        'attached', 'no_local_invoice', 'mandate_revoked', 'ignored', 'failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_mollie_observations_payment
    ON purser.mollie_payment_observations(mollie_payment_id);

CREATE INDEX IF NOT EXISTS idx_mollie_observations_unresolved
    ON purser.mollie_payment_observations(tenant_id, mollie_subscription_id)
    WHERE resolved_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_mollie_observations_invoice
    ON purser.mollie_payment_observations(invoice_id)
    WHERE invoice_id IS NOT NULL;
