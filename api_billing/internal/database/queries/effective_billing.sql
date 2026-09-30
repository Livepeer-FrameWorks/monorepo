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
