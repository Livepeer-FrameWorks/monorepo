-- Payments and reversals are compared with the amount the invoice was
-- presented in, in the original currency the payer was charged in.
CREATE OR REPLACE VIEW purser.payment_report_paid_invoice_amount_mismatch AS
WITH confirmed AS (
    SELECT invoice_id,
           COALESCE(original_currency, UPPER(currency)) AS currency,
           SUM(COALESCE(original_amount_cents, (amount * 100)::bigint)) AS confirmed_payment_cents
    FROM purser.billing_payments
    WHERE status = 'confirmed'
    GROUP BY invoice_id, COALESCE(original_currency, UPPER(currency))
),
reversed AS (
    SELECT invoice_id,
           UPPER(currency) AS currency,
           SUM(amount_cents) AS reversed_payment_cents
    FROM purser.payment_reversals
    WHERE status = 'succeeded'
    GROUP BY invoice_id, UPPER(currency)
)
SELECT bi.id AS invoice_id,
       bi.tenant_id,
       COALESCE(bi.presentment_currency, UPPER(bi.currency))::varchar(3) AS currency,
       COALESCE(bi.presentment_amount_cents, (bi.amount * 100)::bigint) AS invoice_amount_cents,
       COALESCE(c.confirmed_payment_cents, 0) AS confirmed_payment_cents,
       COALESCE(r.reversed_payment_cents, 0) AS reversed_payment_cents
FROM purser.billing_invoices bi
LEFT JOIN confirmed c
    ON c.invoice_id = bi.id AND c.currency = COALESCE(bi.presentment_currency, UPPER(bi.currency))
LEFT JOIN reversed r
    ON r.invoice_id = bi.id AND r.currency = COALESCE(bi.presentment_currency, UPPER(bi.currency))
WHERE bi.status = 'paid'
  AND COALESCE(c.confirmed_payment_cents, 0) - COALESCE(r.reversed_payment_cents, 0)
      <> COALESCE(bi.presentment_amount_cents, (bi.amount * 100)::bigint);
