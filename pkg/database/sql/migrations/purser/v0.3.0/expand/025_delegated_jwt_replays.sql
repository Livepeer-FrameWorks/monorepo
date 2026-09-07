CREATE TABLE IF NOT EXISTS purser.delegated_jwt_replays (
    jti TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_purser_delegated_jwt_replays_expires_at
    ON purser.delegated_jwt_replays (expires_at);
