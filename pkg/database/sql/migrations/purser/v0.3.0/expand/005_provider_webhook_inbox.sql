-- v0.3.0: acknowledge verified provider callbacks after a durable inbox write,
-- then reconcile them out of band with leases and bounded-backoff retries.

CREATE TABLE IF NOT EXISTS purser.provider_webhook_inbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider VARCHAR(50) NOT NULL,
    event_key VARCHAR(320) NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}',
    raw_payload BYTEA NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    last_error TEXT,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(provider, event_key),
    CONSTRAINT chk_provider_webhook_inbox_status
        CHECK (status IN ('pending', 'processing', 'failed', 'processed'))
);

CREATE INDEX IF NOT EXISTS idx_provider_webhook_inbox_pending
    ON purser.provider_webhook_inbox(next_attempt_at, created_at)
    WHERE status IN ('pending', 'failed', 'processing');

CREATE OR REPLACE VIEW purser.payment_report_webhook_inbox_stuck AS
SELECT id, provider, event_key, status, attempts, next_attempt_at,
       last_error, created_at, updated_at
FROM purser.provider_webhook_inbox
WHERE status <> 'processed'
  AND attempts >= 5;
