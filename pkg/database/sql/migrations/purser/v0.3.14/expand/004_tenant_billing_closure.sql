CREATE TABLE IF NOT EXISTS purser.tenant_billing_closures (
    tenant_id UUID PRIMARY KEY,
    closed_at TIMESTAMPTZ NOT NULL,
    source_event_id TEXT NOT NULL,
    provider_subscriptions JSONB NOT NULL DEFAULT '[]'::jsonb,
    providers_cancelled_at TIMESTAMPTZ,
    finalized_at TIMESTAMPTZ
);
