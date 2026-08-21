-- name: GetBootstrapBillingTier :one
SELECT id, COALESCE(tier_level, 0)::integer AS tier_level, currency
FROM purser.billing_tiers
WHERE tier_name = sqlc.arg(tier_name);

-- name: GetBootstrapTenantSubscription :one
SELECT tier_id, billing_model
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: InsertBootstrapTenantSubscription :exec
INSERT INTO purser.tenant_subscriptions (
    id, tenant_id, tier_id, billing_model, status, started_at, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(tier_id),
    sqlc.arg(billing_model), 'active', NOW(), NOW(), NOW()
);

-- name: UpdateBootstrapTenantSubscription :execrows
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(tier_id), billing_model = sqlc.arg(billing_model), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetBootstrapSubscriptionID :one
SELECT id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: DeleteBootstrapEntitlementOverrides :exec
DELETE FROM purser.subscription_entitlement_overrides
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: InsertBootstrapEntitlementOverride :exec
INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
VALUES (sqlc.arg(subscription_id), sqlc.arg(key), sqlc.arg(value)::jsonb);

-- name: EnsureBootstrapPrepaidBalance :exec
INSERT INTO purser.prepaid_balances (
    id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, 0, sqlc.arg(currency), 500, NOW(), NOW()
)
ON CONFLICT (tenant_id, currency) DO NOTHING;

-- name: ListBootstrapEligibleClusters :many
WITH params AS (
    SELECT sqlc.arg(cluster_ids)::text[] AS cluster_ids,
           sqlc.arg(tier_level)::integer AS tier_level
)
SELECT cp.cluster_id, COALESCE(cp.required_tier_level, 0)::integer AS required_tier_level
FROM purser.cluster_pricing cp CROSS JOIN params p
WHERE cp.cluster_id = ANY(p.cluster_ids)
  AND COALESCE(cp.required_tier_level, 0) <= p.tier_level
  AND (COALESCE(cp.allow_free_tier, FALSE) = TRUE OR p.tier_level > 0)
ORDER BY COALESCE(cp.required_tier_level, 0) DESC;

-- name: ListBootstrapPricedClusterIDs :many
SELECT cluster_id
FROM purser.cluster_pricing
ORDER BY cluster_id;
