CREATE TABLE IF NOT EXISTS quartermaster.delegated_jwt_replays (
    jti TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_quartermaster_delegated_jwt_replays_expires_at
    ON quartermaster.delegated_jwt_replays (expires_at);
