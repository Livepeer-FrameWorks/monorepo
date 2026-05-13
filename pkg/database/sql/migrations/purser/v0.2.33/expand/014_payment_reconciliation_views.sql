-- Read-only reconciliation views for payment-state drift checks.
-- Schema source of truth: pkg/database/sql/schema/purser.sql

CREATE OR REPLACE VIEW purser.payment_report_provider_objects_without_local_rows AS
SELECT ppo.*
FROM purser.provider_payment_objects ppo
LEFT JOIN purser.billing_payments bp
    ON ppo.local_reference_type = 'payment'
   AND ppo.local_reference_id = bp.id
LEFT JOIN purser.pending_topups pt
    ON ppo.local_reference_type = 'topup'
   AND ppo.local_reference_id = pt.id
LEFT JOIN purser.payment_provider_intents ppi
    ON ppo.local_reference_type = 'intent'
   AND ppo.local_reference_id = ppi.id
WHERE ppo.local_reference_id IS NOT NULL
  AND (
      (ppo.local_reference_type = 'payment' AND bp.id IS NULL)
      OR (ppo.local_reference_type = 'topup' AND pt.id IS NULL)
      OR (ppo.local_reference_type = 'intent' AND ppi.id IS NULL)
      OR (ppo.local_reference_type NOT IN ('payment', 'topup', 'intent'))
  );

CREATE OR REPLACE VIEW purser.payment_report_pending_failed_intents AS
SELECT *
FROM purser.payment_provider_intents
WHERE status IN ('pending', 'provider_open', 'sca_required', 'provider_call_failed', 'terminal_failed')
  AND updated_at < NOW() - INTERVAL '15 minutes';

CREATE OR REPLACE VIEW purser.payment_report_paid_invoice_amount_mismatch AS
WITH confirmed AS (
    SELECT invoice_id,
           currency,
           SUM((amount * 100)::bigint) AS confirmed_payment_cents
    FROM purser.billing_payments
    WHERE status = 'confirmed'
    GROUP BY invoice_id, currency
),
reversed AS (
    SELECT invoice_id,
           currency,
           SUM(amount_cents) AS reversed_payment_cents
    FROM purser.payment_reversals
    WHERE status = 'succeeded'
    GROUP BY invoice_id, currency
)
SELECT bi.id AS invoice_id,
       bi.tenant_id,
       bi.currency,
       (bi.amount * 100)::bigint AS invoice_amount_cents,
       COALESCE(c.confirmed_payment_cents, 0) AS confirmed_payment_cents,
       COALESCE(r.reversed_payment_cents, 0) AS reversed_payment_cents
FROM purser.billing_invoices bi
LEFT JOIN confirmed c ON c.invoice_id = bi.id AND c.currency = bi.currency
LEFT JOIN reversed r ON r.invoice_id = bi.id AND r.currency = bi.currency
WHERE bi.status = 'paid'
  AND COALESCE(c.confirmed_payment_cents, 0) - COALESCE(r.reversed_payment_cents, 0) <> (bi.amount * 100)::bigint;

CREATE OR REPLACE VIEW purser.payment_report_reversals_without_payment_rows AS
SELECT pr.*
FROM purser.payment_reversals pr
LEFT JOIN purser.billing_payments bp ON pr.payment_id = bp.id
LEFT JOIN purser.pending_topups pt ON pr.pending_topup_id = pt.id
WHERE (pr.payment_id IS NOT NULL AND bp.id IS NULL)
   OR (pr.pending_topup_id IS NOT NULL AND pt.id IS NULL)
   OR (pr.payment_id IS NULL AND pr.pending_topup_id IS NULL);

CREATE OR REPLACE VIEW purser.payment_report_prepaid_negative_balances AS
SELECT *
FROM purser.prepaid_balances
WHERE balance_cents < 0;

CREATE OR REPLACE VIEW purser.payment_report_operator_credits_without_clawback AS
SELECT accrual.*
FROM purser.operator_credit_ledger accrual
JOIN purser.payment_reversals pr
    ON pr.invoice_id = accrual.invoice_id
   AND pr.status = 'succeeded'
LEFT JOIN purser.operator_credit_ledger clawback
    ON clawback.reverses_ledger_id = accrual.id
   AND clawback.entry_type = 'clawback'
WHERE accrual.entry_type = 'accrual'
  AND clawback.id IS NULL;

CREATE OR REPLACE VIEW purser.payment_report_stripe_meter_outbox_stuck AS
SELECT *
FROM purser.stripe_meter_events_outbox
WHERE sent_at IS NULL
  AND attempt_count >= 5;
