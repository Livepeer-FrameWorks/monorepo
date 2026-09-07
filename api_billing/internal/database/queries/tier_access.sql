-- name: ListEligibleOfficialClusters :many
SELECT cluster_id, required_tier_level
FROM purser.cluster_pricing
WHERE cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
  AND required_tier_level <= sqlc.arg(tier_level)::integer
  AND (allow_free_tier = true OR sqlc.arg(tier_level)::integer > 0)
ORDER BY required_tier_level DESC, cluster_id ASC;

-- name: ListSubscriptionTierNames :many
SELECT DISTINCT ON (ts.tenant_id) ts.tenant_id, bt.tier_name,
       CASE WHEN ts.status = 'active' THEN COALESCE(
           (SELECT seo.value FROM purser.subscription_entitlement_overrides seo
            WHERE seo.subscription_id = ts.id AND seo.key = 'custom_subdomain_enabled'),
           (SELECT te.value FROM purser.tier_entitlements te
            WHERE te.tier_id = bt.id AND te.key = 'custom_subdomain_enabled'),
           'false'::jsonb
       )::text::boolean ELSE false END AS custom_subdomain_enabled,
       CASE WHEN ts.status = 'active' THEN COALESCE(
           (SELECT seo.value FROM purser.subscription_entitlement_overrides seo
            WHERE seo.subscription_id = ts.id AND seo.key = 'custom_domain_enabled'),
           (SELECT te.value FROM purser.tier_entitlements te
            WHERE te.tier_id = bt.id AND te.key = 'custom_domain_enabled'),
           'false'::jsonb
       )::text::boolean ELSE false END AS custom_domain_enabled
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
ORDER BY ts.tenant_id, ts.updated_at DESC, ts.created_at DESC, ts.id DESC;

-- name: LoadEffectiveDNSEntitlements :one
SELECT bt.tier_name,
	       CASE WHEN ts.status = 'active' THEN COALESCE(
	           (SELECT seo.value FROM purser.subscription_entitlement_overrides seo
	            WHERE seo.subscription_id = ts.id AND seo.key = 'custom_subdomain_enabled'),
	           (SELECT te.value FROM purser.tier_entitlements te
	            WHERE te.tier_id = bt.id AND te.key = 'custom_subdomain_enabled'),
	           'false'::jsonb
	       )::text::boolean ELSE false END AS custom_subdomain_enabled,
	       CASE WHEN ts.status = 'active' THEN COALESCE(
	           (SELECT seo.value FROM purser.subscription_entitlement_overrides seo
	            WHERE seo.subscription_id = ts.id AND seo.key = 'custom_domain_enabled'),
	           (SELECT te.value FROM purser.tier_entitlements te
	            WHERE te.tier_id = bt.id AND te.key = 'custom_domain_enabled'),
	           'false'::jsonb
	       )::text::boolean ELSE false END AS custom_domain_enabled
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
	ORDER BY ts.updated_at DESC, ts.created_at DESC, ts.id DESC
LIMIT 1;
