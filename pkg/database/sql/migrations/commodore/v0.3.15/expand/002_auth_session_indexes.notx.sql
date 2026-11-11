CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_session_owner ON commodore.refresh_tokens (tenant_id, user_id, session_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_predecessor ON commodore.refresh_tokens (tenant_id, user_id, replaced_by);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_expires_range ON commodore.refresh_tokens (expires_at ASC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_auth_authz_codes_cleanup_range ON commodore.auth_authorization_codes (expires_at ASC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_auth_device_codes_cleanup_range ON commodore.auth_device_codes (expires_at ASC);
