CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_pending_session_range ON commodore.refresh_tokens (expires_at ASC, tenant_id ASC, id ASC)
    WHERE session_id IS NULL AND COALESCE(revoked, false) = false;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_pending_session_owner_range ON commodore.refresh_tokens (tenant_id, expires_at ASC, id ASC)
    WHERE session_id IS NULL AND COALESCE(revoked, false) = false;
