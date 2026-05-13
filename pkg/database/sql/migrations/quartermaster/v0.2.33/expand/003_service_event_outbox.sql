-- Durable outbox for Quartermaster service events emitted to Decklog
-- (tenant + cluster mutations). Producers write a row in the same DB
-- transaction as the state mutation; a drain worker dispatches with
-- exponential backoff. Payload is the full pb.ServiceEvent serialized
-- as protojson — the oneof variants (TenantEvent / ClusterEvent / etc.)
-- ride along inside it.
--
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

CREATE TABLE IF NOT EXISTS quartermaster.service_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    TEXT NOT NULL,
    tenant_id     UUID NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_qm_service_event_outbox_pending
    ON quartermaster.service_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_qm_service_event_outbox_tenant
    ON quartermaster.service_event_outbox(tenant_id, created_at DESC);
