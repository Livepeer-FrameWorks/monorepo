-- name: GetBootstrapTierID :one
SELECT id
FROM purser.billing_tiers
WHERE tier_name = sqlc.arg(tier_name);

-- name: InsertBootstrapBillingTier :one
INSERT INTO purser.billing_tiers (
    tier_name, display_name, description,
    base_price, currency, billing_period,
    features, support_level, sla_level,
    metering_enabled, tier_level, is_enterprise,
    is_default_prepaid, is_default_postpaid,
    processes_live, processes_dvr, processes_clip,
    processes_dvr_finalize, processes_vod
) VALUES (
    sqlc.arg(tier_name), sqlc.arg(display_name), COALESCE(sqlc.arg(description)::text, ''),
    sqlc.arg(base_price)::text::numeric, sqlc.arg(currency), sqlc.arg(billing_period),
    sqlc.arg(features)::jsonb, COALESCE(sqlc.arg(support_level)::text, 'community'),
    COALESCE(sqlc.arg(sla_level)::text, 'none'),
    COALESCE(sqlc.arg(metering_enabled)::boolean, false),
    COALESCE(sqlc.arg(tier_level)::integer, 0), COALESCE(sqlc.arg(is_enterprise)::boolean, false),
    COALESCE(sqlc.arg(is_default_prepaid)::boolean, false),
    COALESCE(sqlc.arg(is_default_postpaid)::boolean, false),
    sqlc.arg(processes_live)::text::jsonb, sqlc.arg(processes_dvr)::text::jsonb,
    sqlc.arg(processes_clip)::text::jsonb, sqlc.arg(processes_dvr_finalize)::text::jsonb,
    sqlc.arg(processes_vod)::text::jsonb
)
RETURNING id;

-- name: GetBootstrapBillingTierState :one
SELECT
    display_name,
    COALESCE(description, '') AS description,
    base_price::text AS base_price,
    currency,
    billing_period,
    features,
    COALESCE(support_level, 'community') AS support_level,
    COALESCE(sla_level, 'none') AS sla_level,
    COALESCE(metering_enabled, false) AS metering_enabled,
    COALESCE(tier_level, 0)::integer AS tier_level,
    COALESCE(is_enterprise, false) AS is_enterprise,
    COALESCE(is_default_prepaid, false) AS is_default_prepaid,
    COALESCE(is_default_postpaid, false) AS is_default_postpaid,
    COALESCE(processes_live, '[]'::jsonb) AS processes_live,
    COALESCE(processes_dvr, '[]'::jsonb) AS processes_dvr,
    COALESCE(processes_clip, '[]'::jsonb) AS processes_clip,
    COALESCE(processes_dvr_finalize, '[]'::jsonb) AS processes_dvr_finalize,
    COALESCE(processes_vod, '[]'::jsonb) AS processes_vod
FROM purser.billing_tiers
WHERE tier_name = sqlc.arg(tier_name);

-- name: UpdateBootstrapBillingTier :execrows
UPDATE purser.billing_tiers
SET display_name = sqlc.arg(display_name),
    description = COALESCE(sqlc.arg(description)::text, ''),
    base_price = sqlc.arg(base_price)::text::numeric,
    currency = sqlc.arg(currency),
    billing_period = sqlc.arg(billing_period),
    features = sqlc.arg(features)::jsonb,
    support_level = COALESCE(sqlc.arg(support_level)::text, 'community'),
    sla_level = COALESCE(sqlc.arg(sla_level)::text, 'none'),
    metering_enabled = COALESCE(sqlc.arg(metering_enabled)::boolean, false),
    tier_level = COALESCE(sqlc.arg(tier_level)::integer, 0),
    is_enterprise = COALESCE(sqlc.arg(is_enterprise)::boolean, false),
    is_default_prepaid = COALESCE(sqlc.arg(is_default_prepaid)::boolean, false),
    is_default_postpaid = COALESCE(sqlc.arg(is_default_postpaid)::boolean, false),
    processes_live = sqlc.arg(processes_live)::text::jsonb,
    processes_dvr = sqlc.arg(processes_dvr)::text::jsonb,
    processes_clip = sqlc.arg(processes_clip)::text::jsonb,
    processes_dvr_finalize = sqlc.arg(processes_dvr_finalize)::text::jsonb,
    processes_vod = sqlc.arg(processes_vod)::text::jsonb
WHERE tier_name = sqlc.arg(tier_name);

-- name: ListBootstrapTierEntitlements :many
SELECT key, value
FROM purser.tier_entitlements
WHERE tier_id = sqlc.arg(tier_id)
ORDER BY key;

-- name: UpsertBootstrapTierEntitlement :exec
INSERT INTO purser.tier_entitlements (tier_id, key, value)
VALUES (sqlc.arg(tier_id), sqlc.arg(key), sqlc.arg(value)::jsonb)
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- name: DeleteBootstrapTierEntitlement :exec
DELETE FROM purser.tier_entitlements
WHERE tier_id = sqlc.arg(tier_id) AND key = sqlc.arg(key);

-- name: ListBootstrapTierPricingRules :many
SELECT meter, model, currency, included_quantity::text AS included_quantity,
       unit_price::text AS unit_price, config
FROM purser.tier_pricing_rules
WHERE tier_id = sqlc.arg(tier_id)
ORDER BY meter;

-- name: UpsertBootstrapTierPricingRule :exec
INSERT INTO purser.tier_pricing_rules (
    tier_id, meter, model, currency, included_quantity, unit_price, config
) VALUES (
    sqlc.arg(tier_id), sqlc.arg(meter), sqlc.arg(model), sqlc.arg(currency),
    sqlc.arg(included_quantity)::text::numeric,
    sqlc.arg(unit_price)::text::numeric,
    sqlc.arg(config)::jsonb
)
ON CONFLICT (tier_id, meter) DO UPDATE SET
    model = EXCLUDED.model,
    currency = EXCLUDED.currency,
    included_quantity = EXCLUDED.included_quantity,
    unit_price = EXCLUDED.unit_price,
    config = EXCLUDED.config;

-- name: DeleteBootstrapTierPricingRule :exec
DELETE FROM purser.tier_pricing_rules
WHERE tier_id = sqlc.arg(tier_id) AND meter = sqlc.arg(meter);
