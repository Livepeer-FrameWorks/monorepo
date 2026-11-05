-- name: InsertNativeMollieTaxInvoice :one
INSERT INTO purser.billing_invoices(tenant_id,status,currency,amount,base_amount,due_date,paid_at,finalized_at,
 usage_details,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,
 source_reference_type,source_reference_id)
VALUES(sqlc.arg(tenant_id)::text::uuid,'paid','EUR',sqlc.arg(gross_minor)::bigint/100.0,sqlc.arg(net_minor)::bigint/100.0,
 sqlc.arg(tax_point)::timestamptz,sqlc.arg(tax_point)::timestamptz,sqlc.arg(tax_point)::timestamptz,
 jsonb_build_object('supply','subscription_base','payment_reference',sqlc.arg(payment_id)::text),
 sqlc.arg(gross_minor)::bigint,'EUR',1,sqlc.arg(tax_point)::timestamptz::date,'mollie_native_payment',sqlc.arg(payment_id))
ON CONFLICT(tenant_id,source_reference_type,source_reference_id) WHERE source_reference_id IS NOT NULL DO NOTHING
RETURNING id::text AS id,invoice_number;

-- name: RecordNativeMollieInvoicePayment :exec
INSERT INTO purser.billing_payments(tenant_id,invoice_id,amount,currency,method,tx_id,status,confirmed_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(invoice_id)::text::uuid,sqlc.arg(gross_minor)::bigint/100.0,'EUR','card',sqlc.arg(payment_id),'confirmed',sqlc.arg(paid_at),sqlc.arg(gross_minor),'EUR',sqlc.arg(gross_minor),1,'identity',sqlc.arg(paid_at)::timestamptz::date)
ON CONFLICT DO NOTHING;

-- name: ListTenantMollieTaxSubscriptions :many
SELECT s.mollie_subscription_id::text AS subscription_id,c.mollie_customer_id::text AS customer_id,
 ROUND(t.base_price*100)::bigint AS net_minor
FROM purser.tenant_subscriptions s
JOIN purser.billing_tiers t ON t.id=s.tier_id
JOIN purser.mollie_customers c ON c.tenant_id=s.tenant_id
WHERE s.tenant_id=sqlc.arg(tenant_id)::text::uuid AND s.mollie_subscription_id IS NOT NULL AND s.mollie_subscription_id<>'' AND s.status NOT IN ('cancelled','ended');

-- name: LockNativeMollieUpgradeSources :many
SELECT i.id::text AS id,i.source_reference_id::text AS payment_id,i.tax_status,
       COALESCE(i.net_eur_cents,0)::bigint AS net_minor,
       COALESCE(i.document_gross_minor,0)::bigint AS gross_minor,
       COALESCE((SELECT SUM(c.net_eur_cents) FROM purser.credit_notes c WHERE c.tenant_id=i.tenant_id AND c.source_document_type='invoice' AND c.source_document_id=i.id),0)::bigint AS credited_net,
       COALESCE((SELECT SUM(c.amount_cents) FROM purser.credit_notes c WHERE c.tenant_id=i.tenant_id AND c.source_document_type='invoice' AND c.source_document_id=i.id),0)::bigint AS credited_gross,
       (i.usage_details->'unused_share_returned' IS NOT NULL)::boolean AS consumed
FROM purser.billing_invoices i
WHERE i.tenant_id=sqlc.arg(tenant_id)::text::uuid AND i.source_reference_type='mollie_native_payment'
  AND i.source_reference_id=ANY(sqlc.arg(payment_ids)::text[])
ORDER BY i.source_reference_id
FOR UPDATE OF i;
