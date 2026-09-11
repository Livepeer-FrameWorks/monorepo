-- name: ReadPlacementSnapshotTime :one
SELECT clock_timestamp()::timestamptz AS observed_at;

-- name: ReadPlacementSubscriptionContext :one
SELECT billing_period_start AS period_start, billing_period_end AS period_end,
       pending_tier_id, pending_effective_at
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND id = sqlc.arg(subscription_id)::text::uuid
  AND status = 'active';

-- name: ListPlacementPricingBoundaries :many
WITH boundaries AS (
    SELECT cluster_id, effective_from AS boundary
    FROM purser.cluster_pricing_history
    WHERE cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
      AND effective_from > sqlc.arg(observed_at)::timestamptz
    UNION ALL
    SELECT cluster_id, effective_to AS boundary
    FROM purser.cluster_pricing_history
    WHERE cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
      AND effective_to > sqlc.arg(observed_at)::timestamptz
)
SELECT cluster_id, MIN(boundary)::timestamptz AS valid_until
FROM boundaries
GROUP BY cluster_id;

-- name: LoadClusterPricingHistory :one
SELECT version_id, pricing_model, currency, base_price::text, metered_rates::text
FROM purser.cluster_pricing_history
WHERE cluster_id = $1
  AND effective_from <= $2
  AND (effective_to IS NULL OR effective_to > $2)
ORDER BY effective_from DESC
LIMIT 1;

-- name: ReadPlacementUsageCoverage :one
WITH sources AS (
    SELECT source_id, active_from, active_until
    FROM purser.metering_sources
    WHERE required = TRUE
      AND active_from < sqlc.arg(cutoff)::timestamptz
      AND (active_until IS NULL OR active_until > sqlc.arg(period_start)::timestamptz)
), expected AS (
    SELECT source_id, window_start
    FROM sources
    CROSS JOIN LATERAL generate_series(
        date_bin(INTERVAL '5 minutes', GREATEST(active_from, sqlc.arg(period_start)::timestamptz), TIMESTAMPTZ '1970-01-01'),
        date_bin(INTERVAL '5 minutes', LEAST(COALESCE(active_until, sqlc.arg(cutoff)::timestamptz), sqlc.arg(cutoff)::timestamptz) - INTERVAL '1 microsecond', TIMESTAMPTZ '1970-01-01'),
        INTERVAL '5 minutes'
    ) AS window_start
)
SELECT (SELECT COUNT(*) FROM sources)::bigint AS active_sources,
       (SELECT COUNT(*) FROM expected e
        LEFT JOIN purser.metering_windows w
          ON w.source_id = e.source_id AND w.period_start = e.window_start
         AND w.period_end = e.window_start + INTERVAL '5 minutes'
         AND w.complete = TRUE AND w.report_count > 0
        WHERE w.source_id IS NULL)::bigint AS missing_windows,
       (SELECT COUNT(*) FROM purser.metering_anomalies
        WHERE status = 'open'
          AND (tenant_id IS NULL OR tenant_id = sqlc.arg(tenant_id)::text::uuid)
          AND created_at <= sqlc.arg(observed_at)::timestamptz)::bigint AS open_anomalies;

-- name: ListPlacementAllowanceUsage :many
WITH usage_rows AS (
    SELECT cluster_id, usage_type, unit, usage_value AS quantity, period_start, period_end
    FROM purser.usage_records
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND (cluster_id = ANY(sqlc.arg(cluster_ids)::text[]) OR cluster_id = '')
      AND usage_type IN ('ingress_gb', 'egress_gb', 'delivered_minutes')
      AND value_kind = 'delta' AND granularity = 'minute_5'
    UNION ALL
    SELECT cluster_id, usage_type, unit, delta_value, period_start, period_end
    FROM purser.usage_adjustments
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND (cluster_id = ANY(sqlc.arg(cluster_ids)::text[]) OR cluster_id = '')
      AND usage_type IN ('ingress_gb', 'egress_gb', 'delivered_minutes')
      AND status = 'applied' AND value_kind = 'correction_delta'
)
SELECT cluster_id, usage_type, SUM(quantity)::text AS quantity,
       BOOL_OR(unit <> CASE WHEN usage_type = 'delivered_minutes' THEN 'minute' ELSE 'gibibyte' END
           OR period_start IS NULL OR period_end IS NULL OR period_start >= period_end
           OR period_end > sqlc.arg(cutoff)::timestamptz) AS invalid_evidence
FROM usage_rows
WHERE (period_start IS NULL OR period_start < sqlc.arg(cutoff)::timestamptz)
  AND (period_end IS NULL OR period_end > sqlc.arg(period_start)::timestamptz)
GROUP BY cluster_id, usage_type;
