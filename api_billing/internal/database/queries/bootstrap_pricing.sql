-- name: GetDefaultMarketplaceFeeBPS :one
SELECT fee_basis_points
FROM purser.platform_fee_policy
WHERE cluster_kind = 'third_party_marketplace'
  AND cluster_owner_tenant_id IS NULL
  AND pricing_source IS NULL
  AND effective_to IS NULL
ORDER BY effective_from DESC
LIMIT 1;

-- name: InsertDefaultMarketplaceFeePolicy :exec
INSERT INTO purser.platform_fee_policy (
    cluster_kind, cluster_owner_tenant_id, pricing_source,
    fee_basis_points, notes
) VALUES (
    'third_party_marketplace', NULL, NULL, $1,
    'default marketplace revenue-share policy'
);

-- name: GetBootstrapClusterPricing :one
SELECT pricing_model,
       COALESCE(base_price, 0)::text AS base_price,
       COALESCE(currency, 'EUR') AS currency,
       COALESCE(required_tier_level, 0)::integer AS required_tier_level,
       COALESCE(allow_free_tier, false) AS allow_free_tier,
       COALESCE(metered_rates, '{}'::jsonb) AS metered_rates,
       COALESCE(default_quotas, '{}'::jsonb) AS default_quotas
FROM purser.cluster_pricing
WHERE cluster_id = $1;

-- name: InsertBootstrapClusterPricing :exec
INSERT INTO purser.cluster_pricing (
    cluster_id, pricing_model, base_price, currency,
    required_tier_level, allow_free_tier, metered_rates, default_quotas,
    updated_at
) VALUES (
    sqlc.arg(cluster_id), sqlc.arg(pricing_model), sqlc.arg(base_price)::numeric,
    sqlc.arg(currency), sqlc.arg(required_tier_level), sqlc.arg(allow_free_tier),
    sqlc.arg(metered_rates)::jsonb, sqlc.arg(default_quotas)::jsonb, NOW()
);

-- name: UpdateBootstrapClusterPricing :exec
UPDATE purser.cluster_pricing
SET pricing_model = sqlc.arg(pricing_model),
    base_price = sqlc.arg(base_price)::numeric,
    currency = sqlc.arg(currency),
    required_tier_level = sqlc.arg(required_tier_level),
    allow_free_tier = sqlc.arg(allow_free_tier),
    metered_rates = sqlc.arg(metered_rates)::jsonb,
    default_quotas = sqlc.arg(default_quotas)::jsonb,
    updated_at = NOW()
WHERE cluster_id = sqlc.arg(cluster_id);
