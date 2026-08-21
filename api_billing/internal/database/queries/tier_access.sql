-- name: ListEligibleOfficialClusters :many
SELECT cluster_id, required_tier_level
FROM purser.cluster_pricing
WHERE cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
  AND required_tier_level <= sqlc.arg(tier_level)::integer
  AND (allow_free_tier = true OR sqlc.arg(tier_level)::integer > 0)
ORDER BY required_tier_level DESC, cluster_id ASC;

-- name: ListSubscriptionTierNames :many
SELECT ts.tenant_id, bt.tier_name
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id;
