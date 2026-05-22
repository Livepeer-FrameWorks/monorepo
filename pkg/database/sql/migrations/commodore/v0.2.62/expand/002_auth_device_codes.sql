-- OAuth 2.0 Device Authorization Grant (RFC 8628) state. CLI clients call
-- /auth/device/start, receive a (device_code, user_code) pair, and poll
-- /auth/device/poll while the user visits /device in any browser to confirm.
-- user_id / tenant_id stay NULL until the user approves; status transitions
-- pending -> approved | denied | expired.
--
-- device_code is stored as SHA-256 hash (BYTEA, 32 bytes); raw device_code
-- never hits disk. user_code is short, dash-formatted (e.g. ABCD-EFGH) and
-- stored plaintext because it is shown to the user.

CREATE TABLE IF NOT EXISTS commodore.auth_device_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID,
    user_id               UUID,
    client_id             VARCHAR(64) NOT NULL,
    device_code_hash      BYTEA NOT NULL,
    user_code             VARCHAR(32) NOT NULL,
    scope                 VARCHAR(64) NOT NULL DEFAULT 'account',
    status                VARCHAR(16) NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    poll_interval_seconds INTEGER NOT NULL DEFAULT 5,
    last_polled_at        TIMESTAMP,
    expires_at            TIMESTAMP NOT NULL,
    approved_at           TIMESTAMP,
    created_at            TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_device_hash
    ON commodore.auth_device_codes(device_code_hash);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_user_code
    ON commodore.auth_device_codes(user_code);

CREATE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_user
    ON commodore.auth_device_codes(user_id)
    WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_auth_device_codes_expires
    ON commodore.auth_device_codes(expires_at)
    WHERE status = 'pending';
