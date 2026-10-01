-- name: UpsertInvoiceLineItem :exec
INSERT INTO purser.invoice_line_items (
    invoice_id, tenant_id, line_key, meter, unit, dimensions, description,
    quantity, included_quantity, billable_quantity,
    unit_price, amount, currency,
    cluster_id, cluster_kind, cluster_owner_tenant_id,
    pricing_source, operator_credit_cents, platform_fee_cents,
    price_version_id, created_at, updated_at
) VALUES (
    sqlc.arg(invoice_id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(line_key), sqlc.narg(meter), sqlc.arg(unit),
    sqlc.arg(dimensions)::jsonb, sqlc.arg(description),
    sqlc.arg(quantity)::text::numeric, sqlc.arg(included_quantity)::text::numeric,
    sqlc.arg(billable_quantity)::text::numeric, sqlc.arg(unit_price)::text::numeric,
    sqlc.arg(amount)::text::numeric, sqlc.arg(currency),
    sqlc.narg(cluster_id), sqlc.narg(cluster_kind), sqlc.narg(cluster_owner_tenant_id),
    sqlc.arg(pricing_source), sqlc.arg(operator_credit_cents), sqlc.arg(platform_fee_cents),
    sqlc.narg(price_version_id), NOW(), NOW()
)
ON CONFLICT (invoice_id, line_key) DO UPDATE SET
    meter = EXCLUDED.meter,
    unit = EXCLUDED.unit,
    dimensions = EXCLUDED.dimensions,
    description = EXCLUDED.description,
    quantity = EXCLUDED.quantity,
    included_quantity = EXCLUDED.included_quantity,
    billable_quantity = EXCLUDED.billable_quantity,
    unit_price = EXCLUDED.unit_price,
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    cluster_id = EXCLUDED.cluster_id,
    cluster_kind = EXCLUDED.cluster_kind,
    cluster_owner_tenant_id = EXCLUDED.cluster_owner_tenant_id,
    pricing_source = EXCLUDED.pricing_source,
    operator_credit_cents = EXCLUDED.operator_credit_cents,
    platform_fee_cents = EXCLUDED.platform_fee_cents,
    price_version_id = EXCLUDED.price_version_id,
    updated_at = NOW();

-- name: ListInvoiceLineKeys :many
SELECT line_key
FROM purser.invoice_line_items
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: DeleteInvoiceLineItem :exec
DELETE FROM purser.invoice_line_items
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND line_key = sqlc.arg(line_key);

-- name: GetMarketplacePlatformFeeBps :one
SELECT fee_basis_points
FROM purser.platform_fee_policy
WHERE cluster_kind = 'third_party_marketplace'
  AND effective_to IS NULL
  AND (cluster_owner_tenant_id = sqlc.arg(owner_id) OR cluster_owner_tenant_id IS NULL)
  AND (pricing_source IS NULL OR pricing_source = sqlc.arg(pricing_source))
ORDER BY (cluster_owner_tenant_id = sqlc.arg(owner_id)) DESC,
         (pricing_source = sqlc.arg(pricing_source)) DESC,
         effective_from DESC
LIMIT 1;

-- name: UpsertManualReviewInvoice :one
INSERT INTO purser.billing_invoices (
    id, tenant_id, amount, currency, status, due_date,
    base_amount, metered_amount, prepaid_credit_applied, usage_details,
    period_start, period_end, gross_metered_amount, created_at, updated_at
) VALUES (
    gen_random_uuid(), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(amount)::text::numeric, sqlc.arg(currency), 'manual_review', sqlc.arg(due_date),
    sqlc.arg(base_amount)::text::numeric, sqlc.arg(metered_amount)::text::numeric,
    sqlc.arg(prepaid_credit_applied)::text::numeric, '{}'::jsonb,
    sqlc.arg(period_start), sqlc.arg(period_end),
    sqlc.arg(gross_metered_amount)::text::numeric, NOW(), NOW()
)
ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
DO UPDATE SET
    amount = EXCLUDED.amount,
    status = 'manual_review',
    due_date = EXCLUDED.due_date,
    base_amount = EXCLUDED.base_amount,
    metered_amount = EXCLUDED.metered_amount,
    period_end = EXCLUDED.period_end,
    gross_metered_amount = EXCLUDED.gross_metered_amount,
    updated_at = NOW()
WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
RETURNING id;
