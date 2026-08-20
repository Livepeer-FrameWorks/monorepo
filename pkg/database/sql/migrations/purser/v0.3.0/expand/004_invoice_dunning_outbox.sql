-- v0.3.0: deliver overdue reminders through the existing durable invoice
-- email outbox, with one idempotent row per configured dunning stage.

ALTER TABLE purser.invoice_email_outbox
    ADD COLUMN IF NOT EXISTS notification_type VARCHAR(32) NOT NULL DEFAULT 'invoice_created',
    ADD COLUMN IF NOT EXISTS reminder_stage INT NOT NULL DEFAULT 0;

ALTER TABLE purser.invoice_email_outbox
    DROP CONSTRAINT IF EXISTS invoice_email_outbox_invoice_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_email_outbox_notification
    ON purser.invoice_email_outbox(invoice_id, notification_type, reminder_stage);

CREATE OR REPLACE VIEW purser.payment_report_invoice_email_outbox_stuck AS
SELECT id, invoice_id, tenant_id, recipient, attempts, next_attempt_at,
       last_error, created_at, updated_at, notification_type, reminder_stage
FROM purser.invoice_email_outbox
WHERE sent_at IS NULL
  AND attempts >= 5;
