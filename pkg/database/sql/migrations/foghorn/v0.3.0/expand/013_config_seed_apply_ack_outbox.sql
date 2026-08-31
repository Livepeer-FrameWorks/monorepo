CREATE TABLE IF NOT EXISTS foghorn.config_seed_apply_ack_outbox (
    id BIGSERIAL PRIMARY KEY,
    node_id VARCHAR(100) NOT NULL UNIQUE,
    cluster_id VARCHAR(100) NOT NULL,
    seed_version BIGINT NOT NULL CHECK (seed_version > 0),
    request_payload BYTEA NOT NULL,
    result_signature BYTEA NOT NULL CHECK (octet_length(result_signature) = 32),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    pending BOOLEAN NOT NULL DEFAULT true,
    pending_since TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(100),
    lease_until TIMESTAMPTZ,
    last_error TEXT,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_config_seed_apply_ack_outbox_due
    ON foghorn.config_seed_apply_ack_outbox(next_attempt_at, id) WHERE pending;
