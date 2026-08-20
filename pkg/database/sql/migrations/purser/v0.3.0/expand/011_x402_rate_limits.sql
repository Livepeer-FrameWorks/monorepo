-- Durable fixed-window abuse controls for quote creation and settlement.

CREATE TABLE IF NOT EXISTS purser.x402_rate_limit_windows (
    scope VARCHAR(32) NOT NULL,
    identity_hash BYTEA NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    request_count INTEGER NOT NULL CHECK (request_count > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(scope, identity_hash, window_started_at)
);

CREATE INDEX IF NOT EXISTS idx_x402_rate_limit_windows_expiry
    ON purser.x402_rate_limit_windows(window_started_at);
