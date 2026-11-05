-- name: GetInvoiceTaxProfile :one
SELECT purser.billing_customer_snapshot(tenant_id)::jsonb AS customer_snapshot,
       COALESCE(billing_email,'')::text AS email, COALESCE(billing_name,'')::text AS name,
       COALESCE(billing_company,'')::text AS company, COALESCE(tax_id,'')::text AS vat_number,
       COALESCE(billing_address,'{}'::jsonb)::jsonb AS address, billing_customer_type::text AS customer_type
FROM purser.tenant_subscriptions
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid FOR SHARE;

-- name: FreezeInvoiceTax :execrows
UPDATE purser.billing_invoices SET
 net_eur_cents=sqlc.arg(net_eur_cents),vat_eur_cents=sqlc.arg(vat_eur_cents),gross_eur_cents=sqlc.arg(gross_eur_cents),
 document_net_minor=sqlc.arg(document_net_minor),document_vat_minor=sqlc.arg(document_vat_minor),document_gross_minor=sqlc.arg(document_gross_minor),
 tax_status=sqlc.arg(tax_status),tax_policy_version=sqlc.arg(tax_policy_version),
 tax_decision=sqlc.arg(tax_decision)::jsonb,tax_summary=sqlc.arg(tax_summary)::jsonb,tax_point_at=sqlc.arg(tax_point_at),
 customer_snapshot=sqlc.arg(customer_snapshot)::jsonb,supplier_snapshot=sqlc.arg(supplier_snapshot)::jsonb
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(invoice_id)::text::uuid
 AND document_kind='invoice' AND tax_status NOT IN ('ready','no_supply');

-- name: ListInvoiceTaxLines :many
SELECT id::text AS id,line_key,ROUND(amount*100)::bigint AS net_eur_cents
FROM purser.invoice_line_items WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND invoice_id=sqlc.arg(invoice_id)::text::uuid
ORDER BY line_key,id;

-- name: FreezeInvoiceLineTax :execrows
UPDATE purser.invoice_line_items SET net_eur_cents=sqlc.arg(net_eur_cents),vat_eur_cents=sqlc.arg(vat_eur_cents),
 gross_eur_cents=sqlc.arg(gross_eur_cents),vat_rate_bps=sqlc.arg(vat_rate_bps),tax_treatment=sqlc.arg(tax_treatment),tax_point_at=sqlc.arg(tax_point_at)
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(id)::text::uuid AND invoice_id=sqlc.arg(invoice_id)::text::uuid;
