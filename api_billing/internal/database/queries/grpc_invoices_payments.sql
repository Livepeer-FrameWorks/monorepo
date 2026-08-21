-- name: GetInvoiceForCaller :one
SELECT invoice.id::text AS id,
       invoice.tenant_id::text AS tenant_id,
       invoice.amount::double precision AS amount,
       invoice.base_amount::double precision AS base_amount,
       invoice.metered_amount::double precision AS metered_amount,
       invoice.prepaid_credit_applied::double precision AS prepaid_credit_applied,
       invoice.currency,
       invoice.status,
       invoice.due_date,
       invoice.paid_at,
       COALESCE(invoice.usage_details, '{}'::jsonb) AS usage_details,
       COALESCE(invoice.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(invoice.updated_at, TIMESTAMPTZ 'epoch') AS updated_at,
       COALESCE(subscription.tier_id::text, '')::text AS tier_id,
       invoice.period_start,
       invoice.period_end,
       invoice.gross_metered_amount::double precision AS gross_metered_amount
FROM purser.billing_invoices invoice
LEFT JOIN purser.tenant_subscriptions subscription
  ON invoice.tenant_id = subscription.tenant_id
 AND subscription.status != 'cancelled'
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND (NOT sqlc.arg(enforce_tenant)::boolean
      OR invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid);

-- name: ListInvoicesForTenant :many
SELECT id::text AS id,
       tenant_id::text AS tenant_id,
       amount::double precision AS amount,
       base_amount::double precision AS base_amount,
       metered_amount::double precision AS metered_amount,
       prepaid_credit_applied::double precision AS prepaid_credit_applied,
       currency,
       status,
       due_date,
       paid_at,
       COALESCE(usage_details, '{}'::jsonb) AS usage_details,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMPTZ 'epoch') AS updated_at,
       period_start,
       period_end,
       gross_metered_amount::double precision AS gross_metered_amount
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_status)::boolean OR status = sqlc.arg(status))
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (sqlc.arg(backward)::boolean
          AND (COALESCE(period_start, created_at), id) >
              (sqlc.arg(cursor_at), sqlc.arg(cursor_id)::text::uuid))
      OR (NOT sqlc.arg(backward)::boolean
          AND (COALESCE(period_start, created_at), id) <
              (sqlc.arg(cursor_at), sqlc.arg(cursor_id)::text::uuid))
  )
ORDER BY
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN COALESCE(period_start, created_at) END DESC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN id END DESC,
    CASE WHEN sqlc.arg(backward)::boolean THEN COALESCE(period_start, created_at) END ASC,
    CASE WHEN sqlc.arg(backward)::boolean THEN id END ASC
LIMIT sqlc.arg(result_limit)::integer;

-- name: GetInvoicePaymentForCaller :one
SELECT payment.id::text AS id,
       payment.invoice_id::text AS invoice_id,
       payment.method,
       payment.amount::double precision AS amount,
       payment.currency,
       payment.tx_id,
       payment.status,
       payment.confirmed_at,
       COALESCE(payment.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(payment.updated_at, TIMESTAMPTZ 'epoch') AS updated_at
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.id = sqlc.arg(payment_id)::text::uuid
  AND (NOT sqlc.arg(enforce_tenant)::boolean
      OR invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid);

-- name: CountInvoicePaymentsForTenant :one
SELECT COUNT(*)
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_invoice)::boolean
      OR payment.invoice_id = sqlc.arg(invoice_id)::text::uuid)
  AND (NOT sqlc.arg(filter_status)::boolean OR payment.status = sqlc.arg(status))
  AND (NOT sqlc.arg(filter_method)::boolean OR payment.method = sqlc.arg(method));

-- name: ListInvoicePaymentsForTenant :many
SELECT payment.id::text AS id,
       payment.invoice_id::text AS invoice_id,
       payment.method,
       payment.amount::double precision AS amount,
       payment.currency,
       payment.tx_id,
       payment.status,
       payment.confirmed_at,
       COALESCE(payment.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(payment.updated_at, TIMESTAMPTZ 'epoch') AS updated_at
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_invoice)::boolean
      OR payment.invoice_id = sqlc.arg(invoice_id)::text::uuid)
  AND (NOT sqlc.arg(filter_status)::boolean OR payment.status = sqlc.arg(status))
  AND (NOT sqlc.arg(filter_method)::boolean OR payment.method = sqlc.arg(method))
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (sqlc.arg(backward)::boolean
          AND (payment.created_at, payment.id) >
              (sqlc.arg(cursor_at), sqlc.arg(cursor_id)::text::uuid))
      OR (NOT sqlc.arg(backward)::boolean
          AND (payment.created_at, payment.id) <
              (sqlc.arg(cursor_at), sqlc.arg(cursor_id)::text::uuid))
  )
ORDER BY
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN payment.created_at END DESC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN payment.id END DESC,
    CASE WHEN sqlc.arg(backward)::boolean THEN payment.created_at END ASC,
    CASE WHEN sqlc.arg(backward)::boolean THEN payment.id END ASC
LIMIT sqlc.arg(result_limit)::integer;
