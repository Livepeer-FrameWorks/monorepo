-- name: GetInvoiceEventState :one
-- The ledger amount and owner of an invoice a settlement just marked paid,
-- read in the settlement's transaction for its billing.invoice_paid event.
SELECT tenant_id::text AS tenant_id, amount::text AS amount
FROM purser.billing_invoices
WHERE id = sqlc.arg(invoice_id)::text::uuid;

-- name: GetPaymentEventAmount :one
-- The EUR amount a payment stores at creation, for its domain events.
SELECT payment.eur_amount_cents
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.id = sqlc.arg(payment_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetTenantBillingDetailsForUpdate :one
-- The billing details UpdateBillingDetails is about to change, read after the
-- subscription row is locked so the changed-field list matches the write.
SELECT COALESCE(billing_email, '') AS billing_email,
       COALESCE(billing_name, '') AS billing_name,
       COALESCE(billing_company, '') AS billing_company,
       COALESCE(tax_id, '') AS tax_id,
       COALESCE(billing_address, '{}'::jsonb) AS billing_address
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;
