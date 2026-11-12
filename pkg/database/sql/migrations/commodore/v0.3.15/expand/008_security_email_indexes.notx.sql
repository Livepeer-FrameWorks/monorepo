CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_session_devices_retention
    ON commodore.auth_session_devices (last_seen_at ASC, tenant_id ASC, user_id ASC, device_key ASC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_account_email_outbox_due
    ON commodore.account_email_outbox (next_attempt_at ASC) WHERE status IN ('pending', 'security_pending');
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_account_email_outbox_terminal
    ON commodore.account_email_outbox (updated_at ASC, tenant_id ASC, user_id ASC, purpose ASC) WHERE status IN ('completed', 'abandoned');
