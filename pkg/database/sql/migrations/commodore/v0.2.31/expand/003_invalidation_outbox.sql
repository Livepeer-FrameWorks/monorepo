-- Durable per-mutation outbox for cross-cluster playback-policy invalidation.
-- One row per signing-key revoke or policy mutation. The worker re-resolves
-- the tenant's cluster footprint each pass and fans out to every cluster
-- whose Foghorn has not yet acknowledged the invalidation.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.playback_policy_invalidation_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    reason TEXT NOT NULL,
    -- Mist internal_names whose sessions need re-evaluation. Empty array means
    -- "every protected playback object the tenant owns" (key-revoke scope).
    internal_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | completed (no terminal abandon — worker retries indefinitely)
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL DEFAULT NOW(),
    last_error TEXT,
    -- Slugs (e.g. "demo-media", "peer-media"). Cluster IDs in this codebase
    -- are operator-defined VARCHAR(100) strings, never UUIDs — see
    -- commodore.streams.active_ingest_cluster_id and pkg/proto cluster fields.
    last_failed_clusters JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commodore_invalidation_outbox_pending
    ON commodore.playback_policy_invalidation_outbox(next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_commodore_invalidation_outbox_tenant
    ON commodore.playback_policy_invalidation_outbox(tenant_id, status);
