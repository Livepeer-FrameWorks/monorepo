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

-- name: CollectInvoiceUsage :many
WITH params AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
           sqlc.arg(window_start)::timestamptz AS window_start,
           sqlc.arg(window_end)::timestamptz AS window_end
), usage_rows AS (
    SELECT COALESCE(ur.cluster_id, '') AS cluster_id, ur.usage_type, ur.usage_value
    FROM purser.usage_records ur CROSS JOIN params p
    WHERE ur.tenant_id = p.tenant_id
      AND ur.period_start < p.window_end
      AND ur.period_end > p.window_start
      AND ur.usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
      AND ur.value_kind = 'delta'
      AND ur.granularity = 'minute_5'
    UNION ALL
    SELECT COALESCE(ua.cluster_id, '') AS cluster_id, ua.usage_type, ua.delta_value AS usage_value
    FROM purser.usage_adjustments ua CROSS JOIN params p
    WHERE ua.tenant_id = p.tenant_id
      AND ua.period_start < p.window_end
      AND ua.period_end > p.window_start
      AND ua.status = 'applied'
      AND ua.value_kind = 'correction_delta'
      AND ua.usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
)
SELECT cluster_id, usage_type,
       (CASE WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers')
             THEN MAX(usage_value) ELSE SUM(usage_value) END)::float8 AS aggregated_value
FROM usage_rows
GROUP BY cluster_id, usage_type;

-- name: CollectInvoiceDimensionedUsage :many
WITH params AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
           sqlc.arg(window_start)::timestamptz AS window_start,
           sqlc.arg(window_end)::timestamptz AS window_end
), dimensioned_rows AS (
    SELECT ur.cluster_id, ur.usage_type, ur.unit, ur.dimensions, ur.usage_value
    FROM purser.usage_records ur CROSS JOIN params p
    WHERE ur.tenant_id = p.tenant_id
      AND ur.period_start < p.window_end
      AND ur.period_end > p.window_start
      AND ur.value_kind = 'delta' AND ur.granularity = 'minute_5'
    UNION ALL
    SELECT ua.cluster_id, ua.usage_type, ua.unit, ua.dimensions, ua.delta_value
    FROM purser.usage_adjustments ua CROSS JOIN params p
    WHERE ua.tenant_id = p.tenant_id
      AND ua.period_start < p.window_end
      AND ua.period_end > p.window_start
      AND ua.status = 'applied' AND ua.value_kind = 'correction_delta'
)
SELECT COALESCE(r.cluster_id, '') AS cluster_id,
       r.usage_type, r.unit, r.dimensions,
       (CASE WHEN d.aggregation = 'max' THEN MAX(r.usage_value)
             ELSE SUM(r.usage_value) END)::float8 AS quantity
FROM dimensioned_rows r
JOIN purser.meter_definitions d ON d.meter = r.usage_type AND d.active = TRUE
GROUP BY r.cluster_id, r.usage_type, r.unit, r.dimensions, d.aggregation;

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
