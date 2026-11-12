CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_owner_active
    ON commodore.auth_sessions (tenant_id, user_id, idle_expires_at ASC, id ASC) WHERE revoked_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_owner_oldest
    ON commodore.auth_sessions (tenant_id, user_id, last_seen_at ASC, created_at ASC, id ASC) WHERE revoked_at IS NULL;
