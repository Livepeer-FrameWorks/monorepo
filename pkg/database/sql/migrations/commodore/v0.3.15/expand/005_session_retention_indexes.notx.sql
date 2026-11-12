CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_retention_idle
    ON commodore.auth_sessions (idle_expires_at ASC, id ASC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_retention_revoked
    ON commodore.auth_sessions (revoked_at ASC, id ASC) WHERE revoked_at IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_retention_session
    ON commodore.refresh_tokens (tenant_id, user_id, session_id, id ASC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_retention_unassigned
    ON commodore.refresh_tokens (expires_at ASC, id ASC) WHERE session_id IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_live_legacy_owner
    ON commodore.refresh_tokens (tenant_id, user_id, expires_at ASC)
    WHERE session_id IS NULL AND COALESCE(revoked, false) = false;
