-- OAuth-style authorization codes for the PKCE browser-handoff login flow
-- (RFC 7636). Tray/CLI clients open the webapp /authorize page; on user
-- approval the webapp asks Commodore to mint a single-use code bound to the
-- caller-supplied code_challenge. The native client then exchanges
-- code + code_verifier at /auth/oauth/token for tokens.
--
-- Codes are stored as SHA-256 hex (VARCHAR(64)); raw codes never hit disk.
-- Single-use: consumed_at is set in the same transaction that issues the
-- JWT. Read-time expiry check is authoritative; sweep is best-effort.

CREATE TABLE IF NOT EXISTS commodore.auth_authorization_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    user_id               UUID NOT NULL,
    client_id             VARCHAR(64) NOT NULL,
    code_hash             VARCHAR(64) NOT NULL,
    code_challenge        VARCHAR(128) NOT NULL,
    code_challenge_method VARCHAR(16) NOT NULL,
    redirect_uri          TEXT NOT NULL,
    scope                 VARCHAR(64) NOT NULL DEFAULT 'account',
    state                 TEXT,
    expires_at            TIMESTAMP NOT NULL,
    consumed_at           TIMESTAMP,
    created_at            TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_hash
    ON commodore.auth_authorization_codes(code_hash);

CREATE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_tenant
    ON commodore.auth_authorization_codes(tenant_id);

CREATE INDEX IF NOT EXISTS idx_commodore_auth_authz_codes_expires
    ON commodore.auth_authorization_codes(expires_at)
    WHERE consumed_at IS NULL;
