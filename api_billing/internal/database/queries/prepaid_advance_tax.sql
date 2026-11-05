-- name: FreezePendingTopupTax :execrows
UPDATE purser.pending_topups SET net_eur_cents=sqlc.arg(net_eur_cents),vat_eur_cents=sqlc.arg(vat_eur_cents),
 gross_eur_cents=sqlc.arg(gross_eur_cents),tax_assessment=sqlc.arg(tax_assessment)::jsonb
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(topup_id)::text::uuid AND status='pending' AND tax_assessment IS NULL;

-- name: InsertFundedAdvanceInvoice :one
INSERT INTO purser.billing_invoices(tenant_id,status,currency,amount,base_amount,due_date,paid_at,finalized_at,
 usage_details,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,
 source_reference_type,source_reference_id)
VALUES(sqlc.arg(tenant_id)::text::uuid,'paid','EUR',sqlc.arg(gross_eur_cents)::bigint/100.0,sqlc.arg(net_eur_cents)::bigint/100.0,
 sqlc.arg(tax_point_at)::timestamptz,sqlc.arg(tax_point_at)::timestamptz,sqlc.arg(tax_point_at)::timestamptz,
 jsonb_build_object('supply','prepaid_service_advance','payment_reference',sqlc.arg(payment_reference)::text),
 sqlc.arg(gross_original_minor)::bigint,sqlc.arg(currency)::text,sqlc.arg(units_per_eur)::text::numeric,sqlc.arg(reference_date)::date,
 sqlc.arg(source_reference_type),sqlc.arg(source_reference_id))
ON CONFLICT(tenant_id,source_reference_type,source_reference_id) WHERE source_reference_id IS NOT NULL DO NOTHING
RETURNING id::text AS id,invoice_number;

-- name: GetFundedAdvanceInvoice :one
SELECT id::text AS id,invoice_number FROM purser.billing_invoices
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND source_reference_type=sqlc.arg(source_reference_type) AND source_reference_id=sqlc.arg(source_reference_id);

-- name: LinkPendingTopupTaxDocument :execrows
UPDATE purser.pending_topups SET tax_document_id=sqlc.arg(invoice_id)::text::uuid
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(topup_id)::text::uuid AND (tax_document_id IS NULL OR tax_document_id=sqlc.arg(invoice_id)::text::uuid);

-- name: GetFundingLotRemaining :one
SELECT COALESCE(SUM(remaining_net_eur_cents),0)::bigint AS remaining
FROM purser.prepaid_funding_lots
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
 AND remaining_net_eur_cents>0;

-- name: InsertFundingLot :exec
INSERT INTO purser.prepaid_funding_lots(tenant_id,source_reference_type,source_reference_id,source_document_id,funding_balance_transaction_id,classification,
 net_credit_eur_cents,remaining_net_eur_cents,vat_eur_cents,gross_eur_cents,tax_assessment,remaining_vat_eur_cents)
VALUES(sqlc.arg(tenant_id)::text::uuid,sqlc.arg(source_reference_type),sqlc.arg(source_reference_id),sqlc.narg(source_document_id)::text::uuid,sqlc.narg(funding_balance_transaction_id)::text::uuid,
 sqlc.arg(classification),sqlc.arg(net_credit_eur_cents)::bigint,sqlc.arg(remaining_net_eur_cents)::bigint,sqlc.narg(vat_eur_cents),sqlc.narg(gross_eur_cents),sqlc.arg(tax_assessment)::jsonb,COALESCE(ROUND(sqlc.narg(vat_eur_cents)::bigint::numeric*sqlc.arg(remaining_net_eur_cents)::bigint/NULLIF(sqlc.arg(net_credit_eur_cents)::bigint,0)),0))
ON CONFLICT(tenant_id,source_reference_type,source_reference_id) DO NOTHING;
