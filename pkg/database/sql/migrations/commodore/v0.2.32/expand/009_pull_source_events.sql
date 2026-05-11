-- Append-only audit of pull-source resolution outcomes. Foghorn writes one
-- row per STREAM_SOURCE evaluation against a pull+ stream — covers the
-- "did Foghorn find the upstream URI / decide to refuse" stage. The
-- downstream MistServer dial outcome is NOT captured here yet (that needs
-- new triggers from MistServer); the resolution stage alone catches most
-- customer-facing misconfiguration (blocked URI, disabled flag, cluster
-- doesn't allow private sources).
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.pull_source_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    stream_id UUID,
    internal_name VARCHAR(255) NOT NULL,
    event_kind VARCHAR(32) NOT NULL,    -- 'resolved' | 'not_found' | 'disabled' | 'blocked_uri' | 'private_not_allowed' | 'commodore_error'
    detail TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_pull_source_events_tenant
    ON commodore.pull_source_events(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commodore_pull_source_events_stream
    ON commodore.pull_source_events(stream_id, created_at DESC)
    WHERE stream_id IS NOT NULL;
