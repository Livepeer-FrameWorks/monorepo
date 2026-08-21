-- name: GetOperatorRevenue :many
SELECT cluster_id, currency,
       SUM(gross_cents)::bigint AS gross_cents,
       SUM(platform_fee_cents)::bigint AS platform_fee_cents,
       SUM(payable_cents)::bigint AS payable_cents,
       COUNT(*) FILTER (WHERE entry_type = 'accrual')::integer AS line_count
FROM purser.operator_credit_ledger
WHERE cluster_owner_tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start < sqlc.arg(range_end)
  AND period_end > sqlc.arg(range_start)
  AND status IN ('accruing', 'eligible', 'paid_out', 'clawed_back')
  AND (NOT sqlc.arg(filter_cluster)::boolean OR cluster_id = sqlc.arg(cluster_id))
GROUP BY cluster_id, currency
ORDER BY cluster_id, currency;

-- name: ListOperatorClusters :many
SELECT cluster_id, currency,
       SUM(gross_cents)::bigint AS gross_cents,
       SUM(platform_fee_cents)::bigint AS platform_fee_cents,
       SUM(payable_cents)::bigint AS payable_cents,
       COUNT(*) FILTER (WHERE entry_type = 'accrual')::integer AS line_count
FROM purser.operator_credit_ledger
WHERE cluster_owner_tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('accruing', 'eligible', 'paid_out', 'clawed_back')
GROUP BY cluster_id, currency
ORDER BY cluster_id, currency;

-- name: ListOperatorPayouts :many
SELECT id, currency, total_cents, status,
       COALESCE(method, '')::text AS method,
       COALESCE(external_reference, '')::text AS external_reference,
       created_at, paid_at
FROM purser.operator_payouts
WHERE cluster_owner_tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_start)::boolean OR created_at >= sqlc.arg(range_start))
  AND (NOT sqlc.arg(filter_end)::boolean OR created_at < sqlc.arg(range_end))
ORDER BY created_at DESC, id DESC;
