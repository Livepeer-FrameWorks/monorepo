-- Commodore-minted signed policy bundle versions. Each row is a signed JWT
-- carrying the tenant's plan, allowed cluster set, JWT verification keys,
-- webhook config, and a monotonic bundle_version. Foghorn caches by version
-- with a soft TTL (background refresh) and a hard TTL (refuse stale past the
-- cap). Revocation rides the existing playback_policy_invalidation_outbox
-- with a 'bundle_revoke' entry carrying the minimum-acceptable bundle_version
-- watermark; Foghorn invalidates cached entries below it. This survives a
-- central Commodore outage for the hard-TTL window without serving stale
-- policy past plan downgrades.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.policy_bundle_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID REFERENCES commodore.streams(id) ON DELETE CASCADE,
    bundle_version BIGINT NOT NULL,
    bundle_jwt TEXT NOT NULL,
    issued_at TIMESTAMP NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMP NOT NULL,
    revoked_at TIMESTAMP,
    UNIQUE (tenant_id, stream_id, bundle_version)
);

CREATE INDEX IF NOT EXISTS idx_commodore_policy_bundle_versions_active
    ON commodore.policy_bundle_versions(tenant_id, stream_id, bundle_version DESC)
    WHERE revoked_at IS NULL;
