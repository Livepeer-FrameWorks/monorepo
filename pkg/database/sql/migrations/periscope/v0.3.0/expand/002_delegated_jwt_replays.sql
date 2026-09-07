CREATE TABLE IF NOT EXISTS periscope.delegated_jwt_replays (
    jti TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_periscope_delegated_jwt_replays_expires_at
    ON periscope.delegated_jwt_replays (expires_at);
