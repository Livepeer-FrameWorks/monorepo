-- name: ListPayableInvoices :many
WITH payable AS MATERIALIZED (
    SELECT bi.id, bi.tenant_id, bi.amount, bi.base_amount, bi.metered_amount,
           bi.prepaid_credit_applied, bi.currency, bi.status, bi.due_date,
           bi.paid_at, bi.usage_details, bi.created_at, bi.updated_at,
           bi.period_start, bi.period_end, bi.gross_metered_amount,
           bi.presentment_amount_cents, bi.presentment_currency,
           bi.presentment_units_per_eur, bi.presentment_reference_date, bi.finalized_at
    FROM purser.billing_invoices bi
    WHERE bi.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND bi.status IN ('pending', 'overdue')
), paid AS MATERIALIZED (
    SELECT bp.invoice_id, bp.currency,
           SUM(bp.amount - COALESCE(bp.reversed_amount_cents, 0)::numeric / 100) AS amount
    FROM purser.billing_payments bp
    WHERE bp.invoice_id = ANY(ARRAY(SELECT id FROM payable))
      AND bp.status = 'confirmed'
      AND (bp.tenant_id = sqlc.arg(tenant_id)::text::uuid OR bp.tenant_id IS NULL)
    GROUP BY bp.invoice_id, bp.currency
)
SELECT bi.id, bi.tenant_id,
       GREATEST(bi.amount - COALESCE(paid.amount, 0), 0)::float8 AS amount_due,
       bi.base_amount::float8 AS base_amount,
       bi.metered_amount::float8 AS metered_amount,
       bi.prepaid_credit_applied::float8 AS prepaid_credit_applied,
       bi.currency, bi.status, bi.due_date, bi.paid_at, bi.usage_details,
       COALESCE(bi.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(bi.updated_at, TIMESTAMPTZ 'epoch') AS updated_at,
       bi.period_start, bi.period_end,
       bi.gross_metered_amount::float8 AS gross_metered_amount,
       bi.presentment_amount_cents,
       COALESCE(bi.presentment_currency, '')::text AS presentment_currency,
       COALESCE(bi.presentment_units_per_eur::text, '')::text AS presentment_units_per_eur,
       bi.presentment_reference_date,
       bi.finalized_at
FROM payable bi
LEFT JOIN paid ON paid.invoice_id = bi.id AND paid.currency = bi.currency
WHERE bi.tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY bi.due_date ASC, bi.id ASC;

-- name: ListRecentPayments :many
SELECT bp.id, bp.invoice_id, bp.method, bp.amount::float8 AS amount, bp.currency,
       bp.tx_id, bp.status, bp.confirmed_at,
       COALESCE(bp.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(bp.updated_at, TIMESTAMPTZ 'epoch') AS updated_at,
       bp.original_amount_cents,
       bp.original_currency::text AS original_currency,
       bp.eur_amount_cents,
       bp.fx_units_per_eur::text AS fx_units_per_eur,
       bp.fx_source,
       bp.fx_reference_date
FROM purser.billing_payments bp
JOIN purser.billing_invoices bi ON bp.invoice_id = bi.id
WHERE bi.tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY bp.created_at DESC, bp.id DESC
LIMIT sqlc.arg(row_limit);
