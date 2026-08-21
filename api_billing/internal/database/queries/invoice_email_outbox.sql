-- name: EnqueueInvoiceEmail :exec
INSERT INTO purser.invoice_email_outbox
    (invoice_id, tenant_id, recipient, notification_type, reminder_stage)
VALUES (
    sqlc.arg(invoice_id)::text::uuid,
    sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(recipient),
    'invoice_created',
    0
)
ON CONFLICT (invoice_id, notification_type, reminder_stage) DO NOTHING;

-- name: ClaimInvoiceEmailCandidates :many
SELECT id, invoice_id, tenant_id, recipient, notification_type, reminder_stage, attempts
FROM purser.invoice_email_outbox
WHERE sent_at IS NULL
  AND next_attempt_at <= NOW()
  AND (claimed_at IS NULL
       OR claimed_at < NOW() - (sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'))
ORDER BY next_attempt_at, created_at
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: LeaseInvoiceEmail :execrows
UPDATE purser.invoice_email_outbox
SET claimed_at = NOW(), lease_token = sqlc.arg(lease_token), updated_at = NOW()
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id);

-- name: CompleteInvoiceEmail :execrows
UPDATE purser.invoice_email_outbox
SET sent_at = NOW(), claimed_at = NULL, lease_token = NULL,
    last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CompleteInvoiceEmailWithLease :execrows
UPDATE purser.invoice_email_outbox
SET sent_at = NOW(), claimed_at = NULL, lease_token = NULL,
    last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: FailInvoiceEmail :execrows
UPDATE purser.invoice_email_outbox
SET attempts = attempts + 1,
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * interval '1 millisecond'),
    last_error = sqlc.arg(last_error), claimed_at = NULL, lease_token = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: FailInvoiceEmailWithLease :execrows
UPDATE purser.invoice_email_outbox
SET attempts = attempts + 1,
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * interval '1 millisecond'),
    last_error = sqlc.arg(last_error), claimed_at = NULL, lease_token = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: GetInvoiceEmailHeader :one
SELECT amount::float8 AS amount,
       metered_amount::float8 AS metered_amount,
       gross_metered_amount::float8 AS gross_metered_amount,
       currency, due_date, status
FROM purser.billing_invoices
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetOverdueInvoiceReminder :one
SELECT GREATEST(bi.amount - COALESCE((
           SELECT SUM(bp.amount - (COALESCE(bp.reversed_amount_cents, 0)::numeric / 100))
           FROM purser.billing_payments bp
           WHERE bp.invoice_id = bi.id
             AND bp.status = 'confirmed'
             AND bp.currency = bi.currency
       ), 0), 0)::float8 AS amount_due,
       bi.currency, bi.due_date, bi.status,
       COALESCE((
           SELECT MAX(candidate)
           FROM UNNEST(ARRAY[1, 7, 14, 30]) AS candidate
           WHERE candidate <= FLOOR(EXTRACT(EPOCH FROM (NOW() - bi.due_date)) / 86400)
       ), 0)::integer AS latest_reminder_stage
FROM purser.billing_invoices bi
WHERE bi.id = sqlc.arg(invoice_id)::text::uuid
  AND bi.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ListInvoiceEmailLineItems :many
SELECT description, unit, dimensions,
       COALESCE(cluster_id, '') AS cluster_id,
       COALESCE(cluster_kind, '') AS cluster_kind,
       quantity::text AS quantity,
       unit_price::text AS unit_price,
       amount::text AS amount,
       currency,
       pricing_source
FROM purser.invoice_line_items
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY (line_key = 'base_subscription') DESC, line_key ASC;
