-- name: GetTenantAdmissionStatus :one
SELECT
    ts.billing_model,
    ts.status AS subscription_status,
    pb.balance_cents,
    reservations.reserved_balance_cents,
    ts.payment_method,
    ts.stripe_subscription_id,
    ts.mollie_subscription_id,
    bt.tier_name,
    COALESCE(bt.tier_level, 0)::integer AS tier_level
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.prepaid_balances pb
    ON pb.tenant_id = ts.tenant_id AND pb.currency = sqlc.arg(currency)
LEFT JOIN LATERAL (
    SELECT CEIL(COALESCE(SUM(reserved_amount_micro), 0)::numeric / 10000)::bigint
        AS reserved_balance_cents
    FROM purser.usage_reservations
    WHERE tenant_id = ts.tenant_id
      AND currency = sqlc.arg(currency)
      AND updated_at >= NOW() - INTERVAL '3 minutes'
) reservations ON TRUE
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: GetTenantBillingStatus :one
SELECT
    ts.billing_model,
    ts.status AS subscription_status,
    pb.balance_cents,
    reservations.reserved_balance_cents,
    COALESCE(te.value::text, '')::text AS retention_value,
    COALESCE(dvr.entitlements::text, '')::text AS dvr_entitlements,
    ts.tier_id::text AS tier_id,
    ts.billing_period_start,
    ts.billing_period_end,
    COALESCE(slg.value::text, '')::text AS storage_limit_value,
    COALESCE(caps.entitlements::text, '')::text AS resource_limits,
    ts.payment_method,
    ts.stripe_subscription_id,
    ts.mollie_subscription_id,
    bt.tier_name
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.prepaid_balances pb
    ON pb.tenant_id = ts.tenant_id AND pb.currency = sqlc.arg(currency)
LEFT JOIN LATERAL (
    SELECT CEIL(COALESCE(SUM(reserved_amount_micro), 0)::numeric / 10000)::bigint
        AS reserved_balance_cents
    FROM purser.usage_reservations
    WHERE tenant_id = ts.tenant_id
      AND currency = sqlc.arg(currency)
      AND updated_at >= NOW() - INTERVAL '3 minutes'
) reservations ON TRUE
LEFT JOIN LATERAL (
    SELECT value FROM (
        SELECT value, 1 AS priority FROM purser.subscription_entitlement_overrides
        WHERE subscription_id = ts.id AND key = 'recording_retention_days'
        UNION ALL
        SELECT value, 2 AS priority FROM purser.tier_entitlements
        WHERE tier_id = ts.tier_id AND key = 'recording_retention_days'
    ) src
    ORDER BY priority
    LIMIT 1
) te ON TRUE
LEFT JOIN LATERAL (
    SELECT jsonb_object_agg(key, value) AS entitlements
    FROM (
        SELECT DISTINCT ON (key) key, value
        FROM (
            SELECT key, value, 1 AS priority
            FROM purser.subscription_entitlement_overrides
            WHERE subscription_id = ts.id AND key = ANY(sqlc.arg(dvr_keys)::text[])
            UNION ALL
            SELECT key, value, 2 AS priority
            FROM purser.tier_entitlements
            WHERE tier_id = ts.tier_id AND key = ANY(sqlc.arg(dvr_keys)::text[])
        ) all_entitlements
        ORDER BY key, priority
    ) merged
) dvr ON TRUE
LEFT JOIN LATERAL (
    SELECT value FROM (
        SELECT value, 1 AS priority FROM purser.subscription_entitlement_overrides
        WHERE subscription_id = ts.id AND key = 'storage_limit_gb'
        UNION ALL
        SELECT value, 2 AS priority FROM purser.tier_entitlements
        WHERE tier_id = ts.tier_id AND key = 'storage_limit_gb'
    ) src
    ORDER BY priority
    LIMIT 1
) slg ON TRUE
LEFT JOIN LATERAL (
    SELECT jsonb_object_agg(key, value) AS entitlements
    FROM (
        SELECT DISTINCT ON (key) key, value
        FROM (
            SELECT key, value, 1 AS priority
            FROM purser.subscription_entitlement_overrides
            WHERE subscription_id = ts.id AND key = ANY(sqlc.arg(resource_keys)::text[])
            UNION ALL
            SELECT key, value, 2 AS priority
            FROM purser.tier_entitlements
            WHERE tier_id = ts.tier_id AND key = ANY(sqlc.arg(resource_keys)::text[])
        ) all_entitlements
        ORDER BY key, priority
    ) merged
) caps ON TRUE
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: GetStoragePricing :one
SELECT
    COALESCE(tpr.included_quantity, 0)::double precision AS included_quantity,
    COALESCE(tpr.unit_price, 0)::double precision AS unit_price,
    COALESCE(tpr.model, '') AS model,
    COALESCE(bt.currency, '') AS currency
FROM purser.tier_pricing_rules tpr
JOIN purser.billing_tiers bt ON bt.id = tpr.tier_id
WHERE tpr.tier_id = sqlc.arg(tier_id)::text::uuid AND tpr.meter = sqlc.arg(meter);

-- name: GetAllowancePricing :one
SELECT
    tpr.included_quantity::double precision AS included_quantity,
    tpr.unit_price::double precision AS unit_price,
    bt.tier_name
FROM purser.tier_pricing_rules tpr
JOIN purser.billing_tiers bt ON bt.id = tpr.tier_id
WHERE tpr.tier_id = sqlc.arg(tier_id)::text::uuid AND tpr.meter = sqlc.arg(meter);

-- name: SumAllowanceUsage :one
SELECT COALESCE(SUM(value), 0)::double precision AS used
FROM (
    SELECT usage_value::double precision AS value
    FROM purser.usage_records ur
    WHERE ur.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND ur.usage_type = sqlc.arg(meter)
      AND ur.value_kind = 'delta'
      AND ur.granularity = 'minute_5'
      AND ur.period_start < sqlc.arg(period_end)
      AND ur.period_end > sqlc.arg(period_start)

    UNION ALL

    SELECT delta_value::double precision AS value
    FROM purser.usage_adjustments ua
    WHERE ua.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND ua.usage_type = sqlc.arg(meter)
      AND ua.value_kind = 'correction_delta'
      AND ua.status = 'applied'
      AND ua.period_start < sqlc.arg(period_end)
      AND ua.period_end > sqlc.arg(period_start)
) allowance_usage;

-- name: GetPrepaidBalance :one
SELECT pb.id, pb.tenant_id, pb.balance_cents, pb.currency,
       COALESCE(pb.low_balance_threshold_cents, 0)::bigint AS low_balance_threshold_cents,
       COALESCE(pb.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(pb.updated_at, TIMESTAMPTZ 'epoch') AS updated_at,
       CEIL(COALESCE(SUM(r.reserved_amount_micro), 0)::numeric / 10000)::bigint
           AS reserved_balance_cents
FROM purser.prepaid_balances pb
LEFT JOIN purser.usage_reservations r
    ON r.tenant_id = pb.tenant_id
    AND r.currency = pb.currency
    AND r.updated_at >= NOW() - INTERVAL '3 minutes'
WHERE pb.tenant_id = sqlc.arg(tenant_id)::text::uuid AND pb.currency = sqlc.arg(currency)
GROUP BY pb.id, pb.tenant_id, pb.balance_cents, pb.currency,
         pb.low_balance_threshold_cents, pb.created_at, pb.updated_at;

-- name: GetPrepaidDrainRate :one
SELECT COALESCE(SUM(ABS(amount_cents)), 0)::bigint AS usage_last_hour
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND transaction_type = 'usage'
  AND amount_cents < 0
  AND created_at >= NOW() - INTERVAL '1 hour';

-- name: ListTenantBillingSnapshots :many
SELECT ts.tenant_id, ts.billing_model, ts.status, ts.tier_id, bt.tier_name,
       ts.trial_ends_at, ts.next_billing_date, ts.created_at,
       COALESCE(pb.balance_cents, 0)::bigint AS prepaid_balance_cents,
       COALESCE(inv.outstanding, 0)::float8 AS outstanding_amount,
       COALESCE(inv.overdue_count, 0)::integer AS overdue_invoices
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.prepaid_balances pb
  ON pb.tenant_id = ts.tenant_id AND pb.currency = sqlc.arg(currency)::text
LEFT JOIN LATERAL (
  SELECT SUM(i.amount) AS outstanding,
         COUNT(*) FILTER (WHERE i.status = 'overdue') AS overdue_count
  FROM purser.billing_invoices i
  WHERE i.tenant_id = ts.tenant_id AND i.status IN ('pending', 'overdue')
) inv ON TRUE
WHERE (cardinality(sqlc.arg(tenant_ids)::text[]) = 0 OR ts.tenant_id::text = ANY(sqlc.arg(tenant_ids)::text[]))
ORDER BY ts.created_at, ts.tenant_id
LIMIT sqlc.arg(row_limit)::int;
