-- name: ListMarketplaceCreditLines :many
SELECT line.id::text AS id, COALESCE(line.cluster_id, '')::text AS cluster_id,
       line.cluster_owner_tenant_id::text AS cluster_owner_tenant_id,
       line.operator_credit_cents, line.platform_fee_cents, line.currency,
       COALESCE(invoice.period_start, invoice.created_at, NOW()) AS period_start,
       COALESCE(invoice.period_end, invoice.period_start, invoice.created_at, NOW()) AS period_end
FROM purser.invoice_line_items line
JOIN purser.billing_invoices invoice ON invoice.id = line.invoice_id
WHERE line.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND line.cluster_kind = 'third_party_marketplace'
  AND line.cluster_owner_tenant_id IS NOT NULL
  AND line.amount != 0
  AND COALESCE(line.meter, '') NOT IN ('storage_gb_seconds_hot', 'storage_gb_seconds_cold');

-- name: InsertMarketplaceOperatorCredit :exec
INSERT INTO purser.operator_credit_ledger (
    source_type, invoice_line_item_id, entry_type,
    cluster_owner_tenant_id, cluster_id,
    invoice_id, period_start, period_end, currency,
    gross_cents, platform_fee_cents, payable_cents, status
) VALUES (
    'invoice_line', sqlc.arg(invoice_line_item_id)::text::uuid, 'accrual',
    sqlc.arg(cluster_owner_tenant_id)::text::uuid, sqlc.arg(cluster_id),
    sqlc.arg(invoice_id)::text::uuid, sqlc.arg(period_start), sqlc.arg(period_end),
    sqlc.arg(currency), sqlc.arg(gross_cents), sqlc.arg(platform_fee_cents),
    sqlc.arg(payable_cents), sqlc.arg(status)
)
ON CONFLICT (invoice_line_item_id)
WHERE entry_type = 'accrual' AND source_type = 'invoice_line'
DO NOTHING;

-- name: ListStorageProviderCreditAllocations :many
WITH provider_lines AS MATERIALIZED (
    SELECT line.id AS line_item_id, line.tenant_id,
           COALESCE(line.cluster_id, '') AS customer_cluster_id,
           line.meter, line.dimensions, line.currency,
           invoice.period_start, invoice.period_end,
           ROUND(line.amount * 100)::bigint AS gross_cents
    FROM purser.invoice_line_items line
    JOIN purser.billing_invoices invoice ON invoice.id = line.invoice_id
    WHERE line.invoice_id = sqlc.arg(invoice_id)::text::uuid
      AND line.tenant_id = invoice.tenant_id
      AND line.meter IS NOT NULL
      AND line.amount != 0
),
relevant_usage AS MATERIALIZED (
    SELECT id,usage_tenant_id,work_cluster_id,provider_tenant_id,provider_cluster_id,
           dimensions,usage_type,usage_value,period_start,period_end
    FROM purser.provider_usage_records
    WHERE usage_tenant_id = ANY(ARRAY(SELECT DISTINCT tenant_id FROM provider_lines))
      AND work_cluster_id = ANY(ARRAY(SELECT DISTINCT customer_cluster_id FROM provider_lines))
      AND usage_type = ANY(ARRAY(SELECT DISTINCT meter FROM provider_lines))
      AND period_start < (SELECT MAX(period_end) FROM provider_lines)
      AND period_end > (SELECT MIN(period_start) FROM provider_lines)
      AND value_kind = 'delta' AND granularity = 'minute_5' AND usage_value != 0
),
relevant_adjustments AS MATERIALIZED (
    SELECT id,tenant_id,cluster_id,details,usage_type,delta_value,period_start,period_end
    FROM purser.usage_adjustments
    WHERE tenant_id = ANY(ARRAY(SELECT DISTINCT tenant_id FROM provider_lines))
      AND cluster_id = ANY(ARRAY(SELECT DISTINCT customer_cluster_id FROM provider_lines))
      AND usage_type = ANY(ARRAY(SELECT DISTINCT meter FROM provider_lines))
      AND period_start < (SELECT MAX(period_end) FROM provider_lines)
      AND period_end > (SELECT MIN(period_start) FROM provider_lines)
      AND value_kind = 'correction_delta' AND status = 'applied' AND delta_value != 0
      AND COALESCE(details #>> '{natural_key,storage_provider_tenant_id}', '') <> ''
),
base_provider_rows AS (
    SELECT provider_line.line_item_id, 'provider_usage'::text AS source_type,
           usage.id::text AS source_id, provider_line.tenant_id AS usage_tenant_id,
           usage.provider_tenant_id::text AS storage_provider_tenant_id,
           usage.provider_cluster_id AS storage_provider_cluster_id,
           COALESCE(usage.dimensions->>'storage_backend', '')::text AS storage_backend,
           usage.usage_type, usage.usage_value AS gb_seconds,
           provider_line.currency, provider_line.period_start, provider_line.period_end,
           provider_line.gross_cents
    FROM provider_lines provider_line
    JOIN relevant_usage usage
      ON usage.usage_tenant_id = provider_line.tenant_id
     AND usage.work_cluster_id = provider_line.customer_cluster_id
     AND usage.usage_type = provider_line.meter
     AND usage.dimensions @> provider_line.dimensions
     AND usage.period_start < provider_line.period_end
     AND usage.period_end > provider_line.period_start
),
adjustment_provider_rows AS (
    SELECT provider_line.line_item_id, 'usage_adjustment'::text AS source_type,
           adjustment.id::text AS source_id, provider_line.tenant_id AS usage_tenant_id,
           COALESCE(adjustment.details #>> '{natural_key,storage_provider_tenant_id}', '') AS storage_provider_tenant_id,
           COALESCE(adjustment.details #>> '{natural_key,storage_provider_cluster_id}', '') AS storage_provider_cluster_id,
           COALESCE(adjustment.details #>> '{natural_key,storage_backend}', '')::text AS storage_backend,
           adjustment.usage_type, adjustment.delta_value AS gb_seconds,
           provider_line.currency, adjustment.period_start, adjustment.period_end,
           provider_line.gross_cents
    FROM provider_lines provider_line
    JOIN relevant_adjustments adjustment
      ON adjustment.tenant_id = provider_line.tenant_id
     AND adjustment.cluster_id = provider_line.customer_cluster_id
     AND adjustment.usage_type = provider_line.meter
     AND adjustment.period_start < provider_line.period_end
     AND adjustment.period_end > provider_line.period_start
),
all_provider_rows AS (
    SELECT rows.*, SUM(gb_seconds) OVER (PARTITION BY line_item_id) AS line_gb_seconds
    FROM (
        SELECT * FROM base_provider_rows
        UNION ALL
        SELECT * FROM adjustment_provider_rows
    ) rows
),
provider_rows AS (
    SELECT * FROM all_provider_rows
    WHERE storage_provider_tenant_id <> ''
      AND storage_provider_tenant_id <> usage_tenant_id::text
)
SELECT source_type, source_id, storage_provider_tenant_id,
       storage_provider_cluster_id, storage_backend, usage_type, currency,
       COALESCE(period_start, NOW()) AS period_start,
       COALESCE(period_end, period_start, NOW()) AS period_end,
       (CASE
         WHEN line_gb_seconds != 0 THEN ROUND(gross_cents::numeric * gb_seconds / line_gb_seconds)::bigint
         ELSE 0
       END)::bigint AS allocated_gross_cents
FROM provider_rows;

-- name: InsertProviderUsageOperatorCredit :exec
INSERT INTO purser.operator_credit_ledger (
    source_type, provider_usage_record_id, entry_type,
    cluster_owner_tenant_id, cluster_id, invoice_id,
    period_start, period_end, currency,
    gross_cents, platform_fee_cents, payable_cents, status, notes
) VALUES (
    'provider_usage', sqlc.arg(provider_usage_record_id)::text::uuid, 'accrual',
    sqlc.arg(cluster_owner_tenant_id)::text::uuid, sqlc.arg(cluster_id), sqlc.arg(invoice_id)::text::uuid,
    sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(currency),
    sqlc.arg(gross_cents), sqlc.arg(platform_fee_cents), sqlc.arg(payable_cents), sqlc.arg(status),
    jsonb_build_object('storage_backend', sqlc.arg(storage_backend)::text, 'usage_type', sqlc.arg(usage_type)::text)
)
ON CONFLICT (provider_usage_record_id)
WHERE entry_type = 'accrual' AND source_type = 'provider_usage'
DO NOTHING;

-- name: InsertUsageAdjustmentOperatorCredit :exec
INSERT INTO purser.operator_credit_ledger (
    source_type, usage_adjustment_id, entry_type,
    cluster_owner_tenant_id, cluster_id, invoice_id,
    period_start, period_end, currency,
    gross_cents, platform_fee_cents, payable_cents, status, notes
) VALUES (
    'usage_adjustment', sqlc.arg(usage_adjustment_id)::text::uuid, 'accrual',
    sqlc.arg(cluster_owner_tenant_id)::text::uuid, sqlc.arg(cluster_id), sqlc.arg(invoice_id)::text::uuid,
    sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(currency),
    sqlc.arg(gross_cents), sqlc.arg(platform_fee_cents), sqlc.arg(payable_cents), sqlc.arg(status),
    jsonb_build_object('storage_backend', sqlc.arg(storage_backend)::text, 'usage_type', sqlc.arg(usage_type)::text)
)
ON CONFLICT (usage_adjustment_id)
WHERE entry_type = 'accrual' AND source_type = 'usage_adjustment'
DO NOTHING;

-- name: InsertStripeSubscriptionOperatorCredit :exec
INSERT INTO purser.operator_credit_ledger (
    source_type, stripe_invoice_id, entry_type,
    cluster_owner_tenant_id, cluster_id,
    period_start, period_end, currency,
    gross_cents, platform_fee_cents, payable_cents, status
) VALUES (
    'stripe_subscription', sqlc.arg(stripe_invoice_id), 'accrual',
    sqlc.arg(cluster_owner_tenant_id)::text::uuid, sqlc.arg(cluster_id),
    sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(currency),
    sqlc.arg(gross_cents), sqlc.arg(platform_fee_cents), sqlc.arg(payable_cents), sqlc.arg(status)
)
ON CONFLICT (stripe_invoice_id)
WHERE entry_type = 'accrual' AND source_type = 'stripe_subscription'
DO NOTHING;

-- name: GetClusterOwnerLedgerState :one
SELECT status, payout_eligible
FROM purser.cluster_owners
WHERE tenant_id = sqlc.arg(owner_id)::text::uuid;

-- name: GetOperatorPlatformFeeBps :one
SELECT fee_basis_points
FROM purser.platform_fee_policy
WHERE cluster_kind = 'third_party_marketplace'
  AND effective_to IS NULL
  AND (cluster_owner_tenant_id = sqlc.arg(owner_id)::text::uuid OR cluster_owner_tenant_id IS NULL)
  AND (pricing_source IS NULL OR pricing_source = sqlc.arg(pricing_source)::text)
ORDER BY (cluster_owner_tenant_id = sqlc.arg(owner_id)::text::uuid) DESC,
         (pricing_source = sqlc.arg(pricing_source)::text) DESC,
         effective_from DESC
LIMIT 1;
