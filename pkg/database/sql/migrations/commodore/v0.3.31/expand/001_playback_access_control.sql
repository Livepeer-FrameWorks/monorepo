-- Playback access control: customer-managed signing keys + per-object playback policy.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.signing_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kid VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL,
    public_key_pem TEXT NOT NULL,
    algorithm VARCHAR(16) NOT NULL DEFAULT 'ES256',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP DEFAULT NOW(),
    last_used_at TIMESTAMP,
    revoked_at TIMESTAMP,
    UNIQUE (tenant_id, kid)
);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_keys_tenant_status
    ON commodore.signing_keys(tenant_id, status);

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT;

CREATE INDEX IF NOT EXISTS idx_commodore_streams_requires_auth
    ON commodore.streams(requires_auth) WHERE requires_auth;
CREATE INDEX IF NOT EXISTS idx_commodore_vod_assets_requires_auth
    ON commodore.vod_assets(requires_auth) WHERE requires_auth;
CREATE INDEX IF NOT EXISTS idx_commodore_clips_requires_auth
    ON commodore.clips(requires_auth) WHERE requires_auth;
