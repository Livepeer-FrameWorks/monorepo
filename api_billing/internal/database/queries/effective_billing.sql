-- name: LoadActiveEffectiveTier :one
SELECT bt.id AS tier_id, bt.tier_name, bt.base_price::text AS base_price,
       bt.currency, COALESCE(bt.metering_enabled, false) AS metering_enabled,
       ts.id AS subscription_id
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ts.status = 'active'
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
