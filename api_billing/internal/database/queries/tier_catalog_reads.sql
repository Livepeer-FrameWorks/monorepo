-- name: ListBillingTiers :many
SELECT id, tier_name, display_name, COALESCE(description, '') AS description,
       base_price::float8 AS base_price, currency, billing_period,
       features, COALESCE(support_level, 'community') AS support_level,
       COALESCE(sla_level, 'none') AS sla_level,
       COALESCE(metering_enabled, false) AS metering_enabled,
       COALESCE(is_active, true) AS is_active,
       COALESCE(tier_level, 0)::integer AS tier_level,
       COALESCE(is_enterprise, false) AS is_enterprise,
       COALESCE(created_at, TIMESTAMP 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at,
       COALESCE(is_default_prepaid, false) AS is_default_prepaid,
       COALESCE(is_default_postpaid, false) AS is_default_postpaid,
       COALESCE(processes_live, '[]'::jsonb) AS processes_live,
       COALESCE(processes_dvr, '[]'::jsonb) AS processes_dvr,
       COALESCE(processes_clip, '[]'::jsonb) AS processes_clip,
       COALESCE(processes_dvr_finalize, '[]'::jsonb) AS processes_dvr_finalize,
       COALESCE(processes_vod, '[]'::jsonb) AS processes_vod
FROM purser.billing_tiers
WHERE (sqlc.arg(include_inactive)::boolean OR COALESCE(is_active, true))
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (sqlc.arg(backward)::boolean AND (COALESCE(tier_level, 0), id) < (sqlc.arg(cursor_tier_level)::integer, sqlc.arg(cursor_id)::text::uuid))
      OR (NOT sqlc.arg(backward)::boolean AND (COALESCE(tier_level, 0), id) > (sqlc.arg(cursor_tier_level)::integer, sqlc.arg(cursor_id)::text::uuid))
  )
ORDER BY
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN COALESCE(tier_level, 0) END ASC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN id END ASC,
    CASE WHEN sqlc.arg(backward)::boolean THEN COALESCE(tier_level, 0) END DESC,
    CASE WHEN sqlc.arg(backward)::boolean THEN id END DESC
LIMIT sqlc.arg(result_limit)::integer;

-- name: GetBillingTierByID :one
SELECT id, tier_name, display_name, COALESCE(description, '') AS description,
       base_price::float8 AS base_price, currency, billing_period,
       features, COALESCE(support_level, 'community') AS support_level,
       COALESCE(sla_level, 'none') AS sla_level,
       COALESCE(metering_enabled, false) AS metering_enabled,
       COALESCE(is_active, true) AS is_active,
       COALESCE(tier_level, 0)::integer AS tier_level,
       COALESCE(is_enterprise, false) AS is_enterprise,
       COALESCE(created_at, TIMESTAMP 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at,
       COALESCE(is_default_prepaid, false) AS is_default_prepaid,
       COALESCE(is_default_postpaid, false) AS is_default_postpaid,
       COALESCE(processes_live, '[]'::jsonb) AS processes_live,
       COALESCE(processes_dvr, '[]'::jsonb) AS processes_dvr,
       COALESCE(processes_clip, '[]'::jsonb) AS processes_clip,
       COALESCE(processes_dvr_finalize, '[]'::jsonb) AS processes_dvr_finalize,
       COALESCE(processes_vod, '[]'::jsonb) AS processes_vod
FROM purser.billing_tiers
WHERE id = sqlc.arg(id)::text::uuid;

-- name: ListActiveMeterDefinitions :many
SELECT meter, unit, aggregation, display_name, allowed_dimensions, default_priceable
FROM purser.meter_definitions
WHERE active = true
ORDER BY meter;
