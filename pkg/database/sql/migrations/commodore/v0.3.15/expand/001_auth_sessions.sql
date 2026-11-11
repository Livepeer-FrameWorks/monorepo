CREATE TABLE IF NOT EXISTS commodore.auth_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID NOT NULL,
    auth_method TEXT NOT NULL,
    client_id TEXT NOT NULL,
    device_label VARCHAR(200) NOT NULL DEFAULT '',
    client_version VARCHAR(100) NOT NULL DEFAULT '',
    created_ip VARCHAR(45) NOT NULL DEFAULT '',
    last_ip VARCHAR(45) NOT NULL DEFAULT '',
    user_agent VARCHAR(1024) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idle_expires_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT,
    CONSTRAINT auth_sessions_owner_identity UNIQUE (tenant_id, user_id, id),
    CONSTRAINT auth_sessions_client_id_check CHECK (client_id IN ('web', 'cli', 'tray-mac', 'unknown')),
    CONSTRAINT auth_sessions_auth_method_check CHECK (auth_method IN ('password', 'wallet', 'pkce', 'device_code', 'unknown')),
    CONSTRAINT auth_sessions_expiry_check CHECK (idle_expires_at <= absolute_expires_at),
    CONSTRAINT auth_sessions_revoke_reason_check CHECK (revoke_reason IN ('user', 'logout', 'password_change', 'password_reset', 'reuse_detected', 'operator', 'expired')),
    CONSTRAINT auth_sessions_revocation_check CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_owner_created
    ON commodore.auth_sessions (tenant_id, user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_idle_expires_range
    ON commodore.auth_sessions (idle_expires_at ASC);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_absolute_expires_range
    ON commodore.auth_sessions (absolute_expires_at ASC);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_revoked_range
    ON commodore.auth_sessions (revoked_at ASC) WHERE revoked_at IS NOT NULL;

ALTER TABLE commodore.refresh_tokens ADD COLUMN IF NOT EXISTS session_id UUID;
ALTER TABLE commodore.refresh_tokens DROP CONSTRAINT IF EXISTS refresh_tokens_auth_session_owner_fk;
ALTER TABLE commodore.refresh_tokens ADD CONSTRAINT refresh_tokens_auth_session_owner_fk
    FOREIGN KEY (tenant_id, user_id, session_id) REFERENCES commodore.auth_sessions (tenant_id, user_id, id) NOT VALID;
ALTER TABLE commodore.auth_authorization_codes ADD COLUMN IF NOT EXISTS device_label VARCHAR(200) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_authorization_codes ADD COLUMN IF NOT EXISTS client_version VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_authorization_codes ADD COLUMN IF NOT EXISTS request_ip VARCHAR(45) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_authorization_codes ADD COLUMN IF NOT EXISTS user_agent VARCHAR(1024) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_device_codes ADD COLUMN IF NOT EXISTS device_label VARCHAR(200) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_device_codes ADD COLUMN IF NOT EXISTS client_version VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_device_codes ADD COLUMN IF NOT EXISTS request_ip VARCHAR(45) NOT NULL DEFAULT '';
ALTER TABLE commodore.auth_device_codes ADD COLUMN IF NOT EXISTS user_agent VARCHAR(1024) NOT NULL DEFAULT '';
