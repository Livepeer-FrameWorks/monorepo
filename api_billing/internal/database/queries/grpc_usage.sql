-- name: GetActiveTenantMaxUsers :one
SELECT COALESCE(override.value, entitlement.value, '0'::jsonb)::text AS max_users
FROM purser.tenant_subscriptions subscription
LEFT JOIN purser.subscription_entitlement_overrides override
  ON override.subscription_id = subscription.id AND override.key = 'max_users'
LEFT JOIN purser.tier_entitlements entitlement
  ON entitlement.tier_id = subscription.tier_id AND entitlement.key = 'max_users'
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND subscription.status = 'active'
ORDER BY subscription.created_at DESC
LIMIT 1;

-- name: ListUsageRecords :many
WITH usage_surface AS (
    SELECT id::text AS id, tenant_id::text AS tenant_id, cluster_id, usage_type,
           unit, COALESCE(dimensions, '{}'::jsonb) AS dimensions,
           usage_value::double precision AS usage_value,
           COALESCE(usage_details, '{}'::jsonb) AS usage_details,
           created_at, period_start, period_end, granularity
    FROM purser.usage_records
    WHERE value_kind = 'delta' AND granularity = 'minute_5'
    UNION ALL
    SELECT id::text AS id, tenant_id::text AS tenant_id, NULLIF(cluster_id, '') AS cluster_id,
           usage_type, unit, COALESCE(dimensions, '{}'::jsonb) AS dimensions,
           delta_value::double precision AS usage_value,
           COALESCE(details, '{}'::jsonb) AS usage_details,
           created_at, period_start, period_end, 'minute_5'::text AS granularity
    FROM purser.usage_adjustments
    WHERE status = 'applied' AND value_kind = 'correction_delta'
)
SELECT id, tenant_id, COALESCE(cluster_id, '')::text AS cluster_id,
       usage_type, unit, dimensions, usage_value, usage_details,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       period_start, period_end, COALESCE(granularity, '')::text AS granularity
FROM usage_surface
WHERE tenant_id = sqlc.arg(tenant_id)::text
  AND (NOT sqlc.arg(filter_cluster)::boolean OR cluster_id = sqlc.arg(cluster_id)::text)
  AND (NOT sqlc.arg(filter_usage_type)::boolean OR usage_type = sqlc.arg(usage_type)::text)
  AND period_start < sqlc.arg(window_end)::timestamptz
  AND period_end > sqlc.arg(window_start)::timestamptz
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (sqlc.arg(backward)::boolean AND (COALESCE(period_start, created_at), id) >
          (sqlc.arg(cursor_at)::timestamptz, sqlc.arg(cursor_id)::text))
      OR (NOT sqlc.arg(backward)::boolean AND (COALESCE(period_start, created_at), id) <
          (sqlc.arg(cursor_at)::timestamptz, sqlc.arg(cursor_id)::text))
  )
ORDER BY
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN COALESCE(period_start, created_at) END DESC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN id END DESC,
    CASE WHEN sqlc.arg(backward)::boolean THEN COALESCE(period_start, created_at) END ASC,
    CASE WHEN sqlc.arg(backward)::boolean THEN id END ASC
LIMIT sqlc.arg(result_limit)::integer;

-- name: ListUsageAggregates :many
WITH usage_surface AS (
    SELECT tenant_id, usage_type, usage_value, period_start, period_end
    FROM purser.usage_records
    WHERE value_kind = 'delta' AND granularity = 'minute_5'
    UNION ALL
    SELECT tenant_id, usage_type, delta_value AS usage_value, period_start, period_end
    FROM purser.usage_adjustments
    WHERE status = 'applied' AND value_kind = 'correction_delta'
), bucketed AS (
    SELECT usage_type,
           CASE sqlc.arg(granularity)::text
               WHEN 'hourly' THEN date_trunc('hour', period_start)
               WHEN 'daily' THEN date_trunc('day', period_start)
               WHEN 'monthly' THEN date_trunc('month', period_start)
           END AS bucket_start,
           usage_value
    FROM usage_surface
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND period_start < sqlc.arg(window_end)::timestamptz
      AND period_end > sqlc.arg(window_start)::timestamptz
      AND (NOT sqlc.arg(filter_usage_types)::boolean OR usage_type = ANY(sqlc.arg(usage_types)::text[]))
)
SELECT bucketed.usage_type, bucket_start::timestamptz AS period_start,
       (bucket_start + CASE sqlc.arg(granularity)::text
           WHEN 'hourly' THEN INTERVAL '1 hour'
           WHEN 'daily' THEN INTERVAL '1 day'
           WHEN 'monthly' THEN INTERVAL '1 month'
       END)::timestamptz AS period_end,
       CASE
           WHEN COALESCE(definition.aggregation, 'sum') = 'max'
               THEN MAX(usage_value)::double precision
           ELSE SUM(usage_value)::double precision
       END AS usage_value,
       sqlc.arg(granularity)::text AS granularity
FROM bucketed
LEFT JOIN purser.meter_definitions definition ON definition.meter = bucketed.usage_type
GROUP BY bucketed.usage_type, bucket_start, COALESCE(definition.aggregation, 'sum')
ORDER BY bucket_start ASC, bucketed.usage_type ASC;

-- name: ListTenantUsageTotals :many
WITH usage_rows AS (
    SELECT COALESCE(cluster_id, '')::text AS cluster_id, usage_type, usage_value AS value
    FROM purser.usage_records
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND period_start < (sqlc.arg(end_date)::date + INTERVAL '1 day')
      AND period_end > sqlc.arg(start_date)::date
      AND value_kind = 'delta' AND granularity = 'minute_5'
    UNION ALL
    SELECT COALESCE(cluster_id, '')::text AS cluster_id, usage_type, delta_value AS value
    FROM purser.usage_adjustments
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND period_start < (sqlc.arg(end_date)::date + INTERVAL '1 day')
      AND period_end > sqlc.arg(start_date)::date
      AND status = 'applied' AND value_kind = 'correction_delta'
)
SELECT cluster_id, usage_type,
       CASE WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers', 'total_streams', 'total_viewers', 'unique_users')
            THEN MAX(value)::double precision ELSE SUM(value)::double precision END AS total
FROM usage_rows
WHERE usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
GROUP BY cluster_id, usage_type;

-- name: ListTenantDimensionedUsage :many
WITH dimensioned_rows AS (
    SELECT COALESCE(cluster_id, '')::text AS cluster_id, usage_type, unit,
           COALESCE(dimensions, '{}'::jsonb) AS dimensions, usage_value
    FROM purser.usage_records
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND period_start < (sqlc.arg(end_date)::date + INTERVAL '1 day')
      AND period_end > sqlc.arg(start_date)::date
      AND value_kind = 'delta' AND granularity = 'minute_5'
    UNION ALL
    SELECT COALESCE(cluster_id, '')::text, usage_type, unit,
           COALESCE(dimensions, '{}'::jsonb), delta_value
    FROM purser.usage_adjustments
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND period_start < (sqlc.arg(end_date)::date + INTERVAL '1 day')
      AND period_end > sqlc.arg(start_date)::date
      AND status = 'applied' AND value_kind = 'correction_delta'
)
SELECT row.cluster_id, row.usage_type, row.unit, row.dimensions,
       (CASE WHEN COALESCE(definition.aggregation, 'sum') = 'max' THEN MAX(row.usage_value)
             ELSE SUM(row.usage_value) END)::text AS quantity
FROM dimensioned_rows row
LEFT JOIN purser.meter_definitions definition ON definition.meter = row.usage_type
GROUP BY row.cluster_id, row.usage_type, row.unit, row.dimensions,
         COALESCE(definition.aggregation, 'sum');
