-- Durable, cross-replica accounting for Purser-owned TicketBroker funding.
-- Every attempted broadcast reserves against the UTC-day cap before signing;
-- uncertain RPC outcomes stay counted so retry storms cannot exceed policy.
CREATE TABLE IF NOT EXISTS purser.livepeer_funding_attempts (
    id BIGSERIAL PRIMARY KEY,
    gateway_address VARCHAR(42) NOT NULL,
    funding_day DATE NOT NULL DEFAULT (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date,
    amount_wei NUMERIC(78, 0) NOT NULL CHECK (amount_wei > 0),
    deposit_wei NUMERIC(78, 0) NOT NULL CHECK (deposit_wei >= 0),
    reserve_wei NUMERIC(78, 0) NOT NULL CHECK (reserve_wei >= 0),
    tx_hash TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_livepeer_funding_attempts_day
    ON purser.livepeer_funding_attempts(funding_day, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_livepeer_funding_attempts_gateway
    ON purser.livepeer_funding_attempts(gateway_address, created_at DESC);
