-- Durable operator queue for crypto settlement conditions that cannot be
-- resolved safely from provider/RPC state alone.

CREATE TABLE IF NOT EXISTS purser.crypto_accounting_anomalies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kind VARCHAR(64) NOT NULL,
    network VARCHAR(64) NOT NULL,
    reference_type VARCHAR(64) NOT NULL,
    reference_id VARCHAR(255) NOT NULL,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    currency CHAR(3) NOT NULL DEFAULT 'EUR',
    detail TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    occurrences INTEGER NOT NULL DEFAULT 1 CHECK (occurrences > 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    resolved_by UUID,
    resolution_note TEXT,
    UNIQUE(kind, reference_type, reference_id)
);

CREATE INDEX IF NOT EXISTS idx_crypto_accounting_anomalies_open
    ON purser.crypto_accounting_anomalies(last_seen_at, tenant_id)
    WHERE status = 'open';
