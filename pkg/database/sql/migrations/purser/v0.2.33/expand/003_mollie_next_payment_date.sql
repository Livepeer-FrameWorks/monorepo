-- Persist Mollie's authoritative next-payment date on the local subscription.
-- The invoice generator anchors Mollie-tenant billing periods on this column
-- so internal period state does not drift from Mollie's actual charge cadence.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS mollie_next_payment_date DATE;
