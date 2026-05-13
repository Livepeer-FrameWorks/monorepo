-- Durable outbox for Foghorn artifact lifecycle + federation peer-registry
-- events emitted to Decklog. Mist-trigger / load-balancing / gateway
-- telemetry remain async and bypass this table — they're loss-tolerant.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

CREATE TABLE IF NOT EXISTS foghorn.artifact_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- event_kind discriminates the typed payload the dispatcher reassembles
    -- before calling the matching decklog method:
    --   clip_lifecycle | dvr_lifecycle | vod_lifecycle | federation_event.
    event_kind   TEXT NOT NULL,
    tenant_id    UUID,
    stream_id    TEXT NOT NULL DEFAULT '',
    artifact_id  TEXT NOT NULL DEFAULT '',
    -- protojson-encoded pb.{ClipLifecycleData | DVRLifecycleData |
    -- VodLifecycleData | FederationEventData} matching event_kind.
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_pending
    ON foghorn.artifact_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_tenant
    ON foghorn.artifact_event_outbox(tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_event_outbox_stream
    ON foghorn.artifact_event_outbox(stream_id, created_at DESC)
    WHERE stream_id <> '';
