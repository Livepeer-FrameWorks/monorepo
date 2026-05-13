-- Durable outbox for Purser service events emitted to Decklog. Producers
-- write a row in the same DB transaction as the billing state mutation;
-- a drain worker dispatches to Decklog with exponential backoff. Failed
-- dispatches retry forever — billing events are not loss-tolerant.
--
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE TABLE IF NOT EXISTS purser.billing_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    TEXT NOT NULL,
    tenant_id     UUID NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    -- protojson-encoded pb.BillingEvent; dispatcher unmarshals into the
    -- BillingEvent variant on the outbound pb.ServiceEvent envelope.
    billing_event JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

-- Worker poll index — completed_at NULL means the row still needs dispatch.
CREATE INDEX IF NOT EXISTS idx_purser_billing_event_outbox_pending
    ON purser.billing_event_outbox(created_at)
    WHERE completed_at IS NULL;

-- Tenant scan for ops debugging.
CREATE INDEX IF NOT EXISTS idx_purser_billing_event_outbox_tenant
    ON purser.billing_event_outbox(tenant_id, created_at DESC);
