-- name: ListBillingDocuments :many
SELECT id::text AS id, kind, document_number, amount_cents, currency, status,
       COALESCE(issued_at, NOW()) AS issued_at, retention_until,
       (eur_amount_cents IS NOT NULL)::boolean AS has_eur_amount,
       COALESCE(eur_amount_cents, 0)::bigint AS eur_amount_cents,
       net_eur_cents, vat_eur_cents,
       COALESCE(units_per_eur, '')::text AS units_per_eur,
       COALESCE(TO_CHAR(fx_reference_date, 'YYYY-MM-DD'), '')::text AS fx_reference_date
FROM (
    SELECT id, 'invoice'::text AS kind, invoice_number AS document_number,
           COALESCE(presentment_amount_cents, ROUND(amount * 100)::bigint)::bigint AS amount_cents,
           COALESCE(presentment_currency, currency)::varchar AS currency, status,
           COALESCE(created_at, NOW()) AS issued_at, retention_until,
           CASE WHEN presentment_amount_cents IS NULL THEN NULL ELSE ROUND(amount * 100)::bigint END AS eur_amount_cents,
           NULL::bigint AS net_eur_cents, NULL::bigint AS vat_eur_cents,
           presentment_units_per_eur::text AS units_per_eur,
           presentment_reference_date::date AS fx_reference_date
    FROM purser.billing_invoices WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status <> 'draft'
    UNION ALL
    SELECT id, 'simplified_invoice', invoice_number, gross_amount_cents, currency,
           tax_validation_status, issued_at, retention_until,
           amount_eur_cents, net_eur_cents, vat_eur_cents, fx_units_per_eur::text, fx_reference_date
    FROM purser.simplified_invoices
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND tax_validation_status <> 'location_review'
    UNION ALL
    SELECT id, 'crypto_invoice', invoice_number, gross_amount_cents, currency,
           tax_validation_status, issued_at, retention_until,
           amount_eur_cents, net_eur_cents, vat_eur_cents, fx_units_per_eur::text, fx_reference_date
    FROM purser.crypto_invoices
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND tax_validation_status <> 'location_review'
    UNION ALL
    SELECT payment.id, 'payment_receipt', 'PAY-' || UPPER(LEFT(REPLACE(payment.id::text, '-', ''), 12)),
           ROUND(payment.amount * 100)::bigint, payment.currency, payment.status,
           COALESCE(payment.confirmed_at, payment.created_at, NOW()), payment.retention_until,
           payment.eur_amount_cents, NULL::bigint, NULL::bigint,
           payment.fx_units_per_eur::text, payment.fx_reference_date
    FROM purser.billing_payments payment
    JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
    WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid AND payment.status = 'confirmed'
    UNION ALL
    SELECT id, 'credit_note', credit_note_number, amount_cents, currency, 'issued', issued_at, retention_until,
           NULL::bigint, NULL::bigint, NULL::bigint, NULL::text, NULL::date
    FROM purser.credit_notes WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
) documents
ORDER BY issued_at DESC, id DESC
LIMIT 1000;

-- name: GetInvoiceDocument :one
SELECT invoice.invoice_number,
       COALESCE(invoice.presentment_amount_cents, ROUND(invoice.amount * 100)::bigint)::bigint AS amount_cents,
       COALESCE(invoice.presentment_currency, invoice.currency)::text AS currency, invoice.status,
       COALESCE(invoice.created_at, NOW()) AS issued_at, invoice.retention_until,
       COALESCE(invoice.period_start, invoice.base_fee_period_start) AS period_start,
       COALESCE(invoice.period_end, invoice.base_fee_period_end) AS period_end, invoice.due_date,
       ROUND(invoice.amount * 100)::bigint AS eur_amount_cents,
       COALESCE(invoice.presentment_units_per_eur::text, '')::text AS presentment_units_per_eur,
       invoice.presentment_reference_date,
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
       payment.eur_amount_cents, payment.fx_units_per_eur::text AS fx_units_per_eur, payment.fx_reference_date,
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
       invoice.amount_eur_cents, COALESCE(invoice.net_eur_cents, 0)::bigint AS net_eur_cents,
       COALESCE(invoice.vat_eur_cents, 0)::bigint AS vat_eur_cents,
       COALESCE(invoice.fx_units_per_eur::text, '')::text AS fx_units_per_eur,
       COALESCE(invoice.fx_reference_date, invoice.issued_at::date)::date AS fx_reference_date,
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
       amount_eur_cents, COALESCE(net_eur_cents, 0)::bigint AS net_eur_cents,
       COALESCE(vat_eur_cents, 0)::bigint AS vat_eur_cents,
       COALESCE(fx_units_per_eur::text, '')::text AS fx_units_per_eur,
       COALESCE(fx_reference_date, issued_at::date)::date AS fx_reference_date,
       reference_type, reference_id, supplier_name, supplier_address,
       supplier_vat_number, supplier_registration_number, service_description,
       service_quantity, service_date, customer_email, customer_name,
       COALESCE(customer_company, '')::text AS customer_company,
       customer_address::text AS customer_address, COALESCE(customer_vat_number, '')::text AS customer_vat
FROM purser.crypto_invoices
WHERE id = sqlc.arg(document_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tax_validation_status <> 'location_review';
