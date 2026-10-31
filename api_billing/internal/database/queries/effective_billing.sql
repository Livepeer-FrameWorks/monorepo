-- name: LoadActiveEffectiveTier :one
-- granted_base_price and waive_usage come from an operator grant in force.
SELECT bt.id AS tier_id, bt.tier_name, bt.base_price::text AS base_price,
       bt.currency, COALESCE(bt.metering_enabled, false) AS metering_enabled,
       ts.id AS subscription_id,
       COALESCE(og.waive_usage, false)::boolean AS waive_usage,
       og.base_price AS granted_base_price
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.subscription_operator_grants og
    ON og.subscription_id = ts.id AND (og.expires_at IS NULL OR og.expires_at > NOW())
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ts.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = ts.tenant_id)
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: LoadSubscriptionEffectiveTier :one
-- LoadActiveEffectiveTier for a subscription in any status: a prepaid period
-- is stated at its tier's prices although the balance suspended the tenant.
SELECT bt.id AS tier_id, bt.tier_name, bt.base_price::text AS base_price,
       bt.currency, COALESCE(bt.metering_enabled, false) AS metering_enabled,
       ts.id AS subscription_id,
       COALESCE(og.waive_usage, false)::boolean AS waive_usage,
       og.base_price AS granted_base_price
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
LEFT JOIN purser.subscription_operator_grants og
    ON og.subscription_id = ts.id AND (og.expires_at IS NULL OR og.expires_at > NOW())
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: LoadSubscriptionEffectiveTierAt :one
-- An invoice covers time before close_at, so a grant beginning exactly there
-- belongs to the next phase; one ending there still prices this phase.
SELECT bt.id AS tier_id, bt.tier_name, bt.base_price::text AS base_price,
       bt.currency, COALESCE(bt.metering_enabled, false) AS metering_enabled,
       ts.id AS subscription_id, COALESCE(og.waive_usage, false)::boolean AS waive_usage,
       og.base_price AS granted_base_price
FROM purser.tenant_subscriptions ts
LEFT JOIN LATERAL (
    SELECT NULL::uuid AS tier_id, g.base_price, g.waive_usage, g.granted_at
    FROM purser.subscription_operator_grants g
    WHERE g.subscription_id = ts.id AND g.granted_at < sqlc.arg(close_at)::timestamptz
      AND (g.expires_at IS NULL OR g.expires_at >= sqlc.arg(close_at)::timestamptz)
    UNION ALL
    SELECT h.tier_id, h.base_price, h.waive_usage, h.granted_at
    FROM purser.subscription_operator_grant_history h
    WHERE h.tenant_id = ts.tenant_id AND h.subscription_id = ts.id
      AND h.granted_at < sqlc.arg(close_at)::timestamptz AND h.ended_at >= sqlc.arg(close_at)::timestamptz
    ORDER BY granted_at DESC LIMIT 1
) og ON TRUE
JOIN purser.billing_tiers bt ON bt.id = COALESCE(og.tier_id, ts.tier_id)
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY ts.created_at DESC LIMIT 1;

-- name: ListTierPricingRules :many
SELECT meter, model, currency, included_quantity::text AS included_quantity,
       unit_price::text AS unit_price, config::text AS config
FROM purser.tier_pricing_rules
WHERE tier_id = sqlc.arg(tier_id)::text::uuid;

-- name: ListSubscriptionPricingOverrides :many
SELECT meter, model, currency, included_quantity, unit_price,
       COALESCE(config, '{}'::jsonb) AS config
FROM purser.subscription_pricing_overrides
WHERE subscription_id = sqlc.arg(subscription_id)::text::uuid;

-- name: ListTierEntitlements :many
SELECT key, value::text AS value
FROM purser.tier_entitlements
WHERE tier_id = sqlc.arg(tier_id)::text::uuid;

-- name: ListSubscriptionEntitlementOverrides :many
SELECT key, value::text AS value
FROM purser.subscription_entitlement_overrides
WHERE subscription_id = sqlc.arg(subscription_id)::text::uuid;
