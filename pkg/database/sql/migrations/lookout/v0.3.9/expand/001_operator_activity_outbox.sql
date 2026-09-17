CREATE TABLE IF NOT EXISTS lookout.operator_activity_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    tenant_id UUID,
    channel TEXT NOT NULL,
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    last_error TEXT,
    delivered_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_operator_activity_channel CHECK (channel IN ('slack', 'discord')),
    CONSTRAINT chk_operator_activity_settled_once CHECK (delivered_at IS NULL OR failed_at IS NULL),
    CONSTRAINT uq_operator_activity_event_channel UNIQUE (source_event_id, channel)
);

CREATE INDEX IF NOT EXISTS idx_operator_activity_pending
    ON lookout.operator_activity_outbox (next_attempt_at, created_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_operator_activity_delivered
    ON lookout.operator_activity_outbox (delivered_at)
    WHERE delivered_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_operator_activity_failed
    ON lookout.operator_activity_outbox (failed_at)
    WHERE failed_at IS NOT NULL;
