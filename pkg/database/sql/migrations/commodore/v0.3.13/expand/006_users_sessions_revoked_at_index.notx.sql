CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_users_sessions_revoked_at
    ON commodore.users(sessions_revoked_at ASC)
    WHERE sessions_revoked_at IS NOT NULL;
