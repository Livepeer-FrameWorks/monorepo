-- Bind each x402 v2 authorization to an immutable tenant top-up offer and
-- preserve provider outcomes across request timeouts and process restarts.

CREATE TABLE IF NOT EXISTS purser.x402_payment_quotes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    resource TEXT NOT NULL,
    resource_class VARCHAR(32) NOT NULL,
    network VARCHAR(64) NOT NULL,
    asset VARCHAR(42) NOT NULL,
    pay_to VARCHAR(42) NOT NULL,
    amount_atomic NUMERIC(78, 0) NOT NULL CHECK (amount_atomic > 0),
    credit_amount_cents BIGINT NOT NULL CHECK (credit_amount_cents > 0),
    credit_currency CHAR(3) NOT NULL DEFAULT 'EUR',
    eur_per_usd_rate NUMERIC(20, 10) NOT NULL CHECK (eur_per_usd_rate > 0),
    requirements_json JSONB NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'offered' CHECK (
        status IN ('offered', 'claiming', 'settling', 'unknown', 'confirmed', 'expired', 'failed')
    ),
    claim_token UUID,
    claim_expires_at TIMESTAMPTZ,
    payment_fingerprint VARCHAR(64),
    provider VARCHAR(32),
    provider_transaction_id VARCHAR(255),
    failure_reason TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_x402_quotes_tenant_created
    ON purser.x402_payment_quotes(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_x402_quotes_reconcile
    ON purser.x402_payment_quotes(status, updated_at)
    WHERE status IN ('claiming', 'settling', 'unknown');
CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_quotes_payment_fingerprint
    ON purser.x402_payment_quotes(payment_fingerprint)
    WHERE payment_fingerprint IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_quotes_provider_transaction
    ON purser.x402_payment_quotes(provider, provider_transaction_id)
    WHERE provider IS NOT NULL AND provider_transaction_id IS NOT NULL;

ALTER TABLE purser.x402_nonces
    ADD COLUMN IF NOT EXISTS quote_id UUID REFERENCES purser.x402_payment_quotes(id),
    ADD COLUMN IF NOT EXISTS settlement_provider VARCHAR(32),
    ADD COLUMN IF NOT EXISTS provider_transaction_id VARCHAR(255);

CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_nonces_quote
    ON purser.x402_nonces(quote_id)
    WHERE quote_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.x402_settlement_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    settlement_id UUID NOT NULL REFERENCES purser.x402_nonces(id),
    attempt_number INTEGER NOT NULL DEFAULT 1 CHECK (attempt_number > 0),
    network VARCHAR(20) NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    relayer_address VARCHAR(42) NOT NULL,
    relayer_nonce BIGINT NOT NULL CHECK (relayer_nonce >= 0),
    signed_raw_transaction BYTEA NOT NULL,
    transaction_hash VARCHAR(66) NOT NULL,
    gas_limit BIGINT NOT NULL CHECK (gas_limit > 0),
    max_fee_per_gas NUMERIC(78,0) NOT NULL CHECK (max_fee_per_gas > 0),
    max_priority_fee_per_gas NUMERIC(78,0) NOT NULL CHECK (max_priority_fee_per_gas >= 0),
    state VARCHAR(24) NOT NULL DEFAULT 'prepared' CHECK (
        state IN ('prepared', 'broadcast_unknown', 'broadcast', 'confirmed', 'reverted', 'replaced')
    ),
    broadcast_attempts INTEGER NOT NULL DEFAULT 0 CHECK (broadcast_attempts >= 0),
    last_error TEXT,
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_broadcast_at TIMESTAMPTZ,
    last_broadcast_at TIMESTAMPTZ,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (settlement_id, attempt_number),
    UNIQUE (network, relayer_address, relayer_nonce),
    UNIQUE (transaction_hash)
);

CREATE INDEX IF NOT EXISTS idx_x402_settlement_attempts_reconcile
    ON purser.x402_settlement_attempts(state, updated_at)
    WHERE state IN ('prepared', 'broadcast_unknown', 'broadcast');
