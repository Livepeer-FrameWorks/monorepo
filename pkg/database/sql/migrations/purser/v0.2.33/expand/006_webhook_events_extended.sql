-- Webhook idempotency is upgraded from a "row exists" marker to a durable
-- claim/lock surface. status='claimed' is taken inside the same transaction
-- that does the reconciliation work; on commit it advances to 'processed',
-- and on retryable failure to 'failed_retryable' with last_error/retry_count
-- populated. raw_payload/signature_header are retained so a webhook can be
-- replayed for debugging without re-pulling from the provider. Pre-existing
-- rows are flipped to 'processed' implicitly via the DEFAULT.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.webhook_events
    ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'processed',
    ADD COLUMN IF NOT EXISTS raw_payload BYTEA,
    ADD COLUMN IF NOT EXISTS signature_header TEXT,
    ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS provider_object_id VARCHAR(255);

ALTER TABLE purser.webhook_events
    ADD CONSTRAINT chk_webhook_events_status CHECK (status IN (
        'claimed', 'processed', 'failed_retryable', 'failed_terminal', 'blocked'
    )) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_webhook_events_status_received
    ON purser.webhook_events(status, received_at)
    WHERE status IN ('claimed', 'failed_retryable', 'blocked');

CREATE INDEX IF NOT EXISTS idx_webhook_events_provider_object
    ON purser.webhook_events(provider, provider_object_id)
    WHERE provider_object_id IS NOT NULL;
