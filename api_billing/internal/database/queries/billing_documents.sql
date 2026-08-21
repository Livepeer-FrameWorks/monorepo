-- name: ListBillingDocuments :many
SELECT id::text AS id, kind, document_number, amount_cents, currency, status,
       COALESCE(issued_at, NOW()) AS issued_at, retention_until
FROM (
    SELECT id, 'invoice'::text AS kind, invoice_number AS document_number,
           ROUND(amount * 100)::bigint AS amount_cents, currency, status,
           COALESCE(created_at, NOW()) AS issued_at, retention_until
    FROM purser.billing_invoices WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status <> 'draft'
    UNION ALL
    SELECT id, 'simplified_invoice', invoice_number, gross_amount_cents, currency,
           tax_validation_status, issued_at, retention_until
    FROM purser.simplified_invoices
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND tax_validation_status <> 'location_review'
    UNION ALL
    SELECT id, 'crypto_invoice', invoice_number, gross_amount_cents, currency,
           tax_validation_status, issued_at, retention_until
    FROM purser.crypto_invoices
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND tax_validation_status <> 'location_review'
    UNION ALL
    SELECT payment.id, 'payment_receipt', 'PAY-' || UPPER(LEFT(REPLACE(payment.id::text, '-', ''), 12)),
           ROUND(payment.amount * 100)::bigint, payment.currency, payment.status,
           COALESCE(payment.confirmed_at, payment.created_at, NOW()), payment.retention_until
    FROM purser.billing_payments payment
    JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
    WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid AND payment.status = 'confirmed'
    UNION ALL
    SELECT id, 'credit_note', credit_note_number, amount_cents, currency, 'issued', issued_at, retention_until
    FROM purser.credit_notes WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
) documents
ORDER BY issued_at DESC, id DESC
LIMIT 1000;

-- name: GetInvoiceDocument :one
SELECT invoice.invoice_number, ROUND(invoice.amount * 100)::bigint AS amount_cents,
       invoice.currency, invoice.status,
       COALESCE(invoice.created_at, NOW()) AS issued_at, invoice.retention_until,
       invoice.period_start, invoice.period_end, invoice.due_date,
       COALESCE(subscription.billing_name, '')::text AS customer_name,
       COALESCE(subscription.billing_company, '')::text AS customer_company,
       COALESCE(subscription.billing_address::text, '')::text AS customer_address,
       COALESCE(subscription.tax_id, '')::text AS customer_vat
FROM purser.billing_invoices invoice
LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE invoice.id = sqlc.arg(document_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.status <> 'draft';

-- name: GetPaymentReceiptDocument :one
SELECT ('PAY-' || UPPER(LEFT(REPLACE(payment.id::text, '-', ''), 12)))::text AS document_number,
       ROUND(payment.amount * 100)::bigint AS amount_cents, payment.currency, payment.status,
       COALESCE(payment.confirmed_at, payment.created_at, NOW()) AS issued_at, payment.retention_until,
       payment.method, payment.tx_id,
       COALESCE(subscription.billing_name, '')::text AS customer_name,
       COALESCE(subscription.billing_company, '')::text AS customer_company,
       COALESCE(subscription.billing_address::text, '')::text AS customer_address,
       COALESCE(subscription.tax_id, '')::text AS customer_vat
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE payment.id = sqlc.arg(document_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND payment.status = 'confirmed';

-- name: GetCreditNoteDocument :one
SELECT note.credit_note_number, note.amount_cents, note.currency, note.issued_at, note.retention_until,
       note.source_document_type, note.source_document_id::text AS source_document_id,
       note.reversal_reference_type, note.reversal_reference_id, note.reason,
       COALESCE(subscription.billing_name, '')::text AS customer_name,
       COALESCE(subscription.billing_company, '')::text AS customer_company,
       COALESCE(subscription.billing_address::text, '')::text AS customer_address,
       COALESCE(subscription.tax_id, '')::text AS customer_vat
FROM purser.credit_notes note
LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = note.tenant_id
WHERE note.id = sqlc.arg(document_id)::text::uuid
  AND note.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetSimplifiedInvoiceDocument :one
SELECT invoice.invoice_number, invoice.gross_amount_cents, invoice.currency,
       invoice.tax_validation_status, invoice.issued_at, invoice.retention_until,
       invoice.net_amount_cents, invoice.vat_amount_cents, invoice.vat_rate_bps,
       invoice.reference_type, invoice.reference_id,
       invoice.supplier_name, invoice.supplier_address, invoice.supplier_vat_number,
       COALESCE(invoice.supplier_registration_number, '')::text AS supplier_registration_number,
       COALESCE(invoice.service_description, 'FrameWorks prepaid usage credit')::text AS service_description,
       COALESCE(invoice.service_quantity, 1)::integer AS service_quantity,
       COALESCE(invoice.service_date, invoice.issued_at::date) AS service_date,
       COALESCE(subscription.billing_name, '')::text AS customer_name,
       COALESCE(subscription.billing_company, '')::text AS customer_company,
       COALESCE(subscription.billing_address::text, '')::text AS customer_address,
       COALESCE(subscription.tax_id, '')::text AS customer_vat
FROM purser.simplified_invoices invoice
LEFT JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE invoice.id = sqlc.arg(document_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.tax_validation_status <> 'location_review';

-- name: GetCryptoInvoiceDocument :one
SELECT invoice_number, gross_amount_cents, currency, tax_validation_status,
       issued_at, retention_until, net_amount_cents, vat_amount_cents, vat_rate_bps,
       reference_type, reference_id, supplier_name, supplier_address,
       supplier_vat_number, supplier_registration_number, service_description,
       service_quantity, service_date, customer_email, customer_name,
       COALESCE(customer_company, '')::text AS customer_company,
       customer_address::text AS customer_address, COALESCE(customer_vat_number, '')::text AS customer_vat
FROM purser.crypto_invoices
WHERE id = sqlc.arg(document_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tax_validation_status <> 'location_review';
