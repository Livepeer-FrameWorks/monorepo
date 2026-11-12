CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_session_cap_cursor
    ON commodore.users (id ASC) INCLUDE (tenant_id);
