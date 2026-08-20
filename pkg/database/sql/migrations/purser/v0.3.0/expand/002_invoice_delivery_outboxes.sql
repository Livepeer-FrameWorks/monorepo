-- v0.3.0: make invoice email delivery durable and preserve each dimensioned
-- invoice line as a distinct Stripe meter-event outbox record.

ALTER TABLE purser.stripe_meter_events_outbox
    ADD COLUMN IF NOT EXISTS invoice_line_item_id UUID
        REFERENCES purser.invoice_line_items(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS dimensions JSONB NOT NULL DEFAULT '{}';

-- CREATE OR REPLACE cannot reorder the columns frozen by the old SELECT *.
-- Recreate the operator-only report with a stable explicit projection so the
-- upgrade path and a fresh baseline have identical definitions.
DROP VIEW IF EXISTS purser.payment_report_stripe_meter_outbox_stuck;
CREATE VIEW purser.payment_report_stripe_meter_outbox_stuck AS
SELECT
    id,
    tenant_id,
    cluster_id,
    meter,
    stripe_meter_event_name,
    quantity,
    dimensions,
    period_start,
    period_end,
    invoice_id,
    invoice_line_item_id,
    sent_at,
    attempt_count,
    last_error,
    created_at,
    updated_at
FROM purser.stripe_meter_events_outbox
WHERE sent_at IS NULL
  AND attempt_count >= 5;

CREATE UNIQUE INDEX IF NOT EXISTS uq_stripe_meter_events_outbox_invoice_line
    ON purser.stripe_meter_events_outbox(invoice_line_item_id, stripe_meter_event_name);

CREATE TABLE IF NOT EXISTS purser.invoice_email_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    recipient TEXT NOT NULL,
    notification_type VARCHAR(32) NOT NULL DEFAULT 'invoice_created',
    reminder_stage INT NOT NULL DEFAULT 0,
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_email_outbox_notification
    ON purser.invoice_email_outbox(invoice_id, notification_type, reminder_stage);
CREATE INDEX IF NOT EXISTS idx_invoice_email_outbox_pending
    ON purser.invoice_email_outbox(next_attempt_at, created_at)
    WHERE sent_at IS NULL;

CREATE OR REPLACE VIEW purser.payment_report_invoice_email_outbox_stuck AS
SELECT id, invoice_id, tenant_id, recipient, attempts, next_attempt_at,
       last_error, created_at, updated_at, notification_type, reminder_stage
FROM purser.invoice_email_outbox
WHERE sent_at IS NULL
  AND attempts >= 5;
