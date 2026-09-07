CREATE TABLE IF NOT EXISTS commodore.delegated_jwt_replays (
    jti TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_delegated_jwt_replays_expires_at
    ON commodore.delegated_jwt_replays (expires_at);
