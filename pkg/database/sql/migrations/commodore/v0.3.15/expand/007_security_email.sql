ALTER TABLE commodore.auth_sessions ADD COLUMN IF NOT EXISTS device_key TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS commodore.auth_session_devices (
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    device_key TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, device_key)
);

ALTER TABLE commodore.account_email_outbox ADD COLUMN IF NOT EXISTS payload JSONB;
ALTER TABLE commodore.account_email_outbox DROP CONSTRAINT IF EXISTS chk_commodore_account_email_outbox_purpose;
ALTER TABLE commodore.account_email_outbox ADD CONSTRAINT chk_commodore_account_email_outbox_purpose
    CHECK (purpose = 'verification' OR purpose ~ '^security_(sign_in|reuse):[0-9a-f-]{36}$') NOT VALID;
ALTER TABLE commodore.account_email_outbox DROP CONSTRAINT IF EXISTS chk_commodore_account_email_outbox_status;
ALTER TABLE commodore.account_email_outbox ADD CONSTRAINT chk_commodore_account_email_outbox_status
    CHECK (status IN ('pending', 'security_pending', 'completed', 'abandoned')) NOT VALID;
ALTER TABLE commodore.account_email_outbox DROP CONSTRAINT IF EXISTS chk_commodore_account_email_outbox_security;
ALTER TABLE commodore.account_email_outbox ADD CONSTRAINT chk_commodore_account_email_outbox_security
    CHECK ((purpose = 'verification' AND status <> 'security_pending') OR
           (purpose <> 'verification' AND status <> 'pending' AND payload IS NOT NULL AND jsonb_typeof(payload) = 'object')) NOT VALID;
