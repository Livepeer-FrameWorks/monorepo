CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_retention_assigned
    ON commodore.refresh_tokens (expires_at ASC, id ASC) WHERE session_id IS NOT NULL;
