-- paid_at is preserved as "first reached fully-paid"; reopened_at records
-- the most recent transition out of paid (refund pushed net paid below
-- the invoice amount). The settlement helper also denormalizes the running
-- confirmed and reversed totals so partial-payment-aware checks do not need
-- to re-aggregate billing_payments on every webhook.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS reopened_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS confirmed_paid_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reversed_paid_cents BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_billing_invoices_reopened
    ON purser.billing_invoices(tenant_id, reopened_at)
    WHERE reopened_at IS NOT NULL;
