-- name: GetClusterPricingConfig :one
SELECT id::text AS id, cluster_id, pricing_model,
       stripe_product_id, stripe_price_id_monthly, stripe_meter_event_name,
       COALESCE(base_price::text, '')::text AS base_price, currency,
       COALESCE(metered_rates, '{}'::jsonb) AS metered_rates,
       required_tier_level, allow_free_tier,
       COALESCE(default_quotas, '{}'::jsonb) AS default_quotas,
       COALESCE(created_at, TIMESTAMP 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at
FROM purser.cluster_pricing
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: GetActiveTenantTierLevel :one
SELECT COALESCE(tier.tier_level, 0)::int AS tier_level
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON subscription.tier_id = tier.id
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND subscription.status = 'active';

-- name: ListClusterPricingConfigs :many
SELECT id::text AS id, cluster_id, pricing_model,
       stripe_product_id, stripe_price_id_monthly, stripe_meter_event_name,
       COALESCE(base_price::text, '')::text AS base_price, currency,
       COALESCE(metered_rates, '{}'::jsonb) AS metered_rates,
       required_tier_level, allow_free_tier,
       COALESCE(default_quotas, '{}'::jsonb) AS default_quotas,
       COALESCE(created_at, TIMESTAMP 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at
FROM purser.cluster_pricing
WHERE NOT sqlc.arg(filter_clusters)::boolean
   OR cluster_id = ANY(sqlc.arg(cluster_ids)::text[])
ORDER BY created_at DESC
LIMIT sqlc.arg(result_limit);

-- name: UpsertStripeClusterCheckoutIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, idempotency_key
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'stripe', 'cluster_subscription_checkout',
    'cluster', NULL, 'pending', sqlc.arg(currency), sqlc.arg(idempotency_key)
)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET
    attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: SetClusterCheckoutIntentCustomer :exec
UPDATE purser.payment_provider_intents
SET provider_customer_id = sqlc.arg(customer_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: SetClusterCheckoutIntentSessionOpen :exec
UPDATE purser.payment_provider_intents
SET provider_session_id = sqlc.arg(session_id), status = 'provider_open', updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: UpsertPendingClusterCheckoutSubscription :one
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, stripe_customer_id, checkout_session_id,
    intent_id, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), 'pending_payment',
    sqlc.arg(customer_id), sqlc.arg(session_id), sqlc.arg(intent_id)::text::uuid,
    NOW(), NOW()
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    status = EXCLUDED.status,
    stripe_customer_id = EXCLUDED.stripe_customer_id,
    checkout_session_id = EXCLUDED.checkout_session_id,
    intent_id = EXCLUDED.intent_id,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: GetClusterStripeSubscriptionID :one
SELECT stripe_subscription_id
FROM purser.cluster_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND cluster_id = sqlc.arg(cluster_id);

-- name: UpdateCancelledClusterSubscription :exec
UPDATE purser.cluster_subscriptions
SET status = sqlc.arg(status)::varchar(50),
    stripe_subscription_status = sqlc.arg(stripe_status),
    stripe_current_period_end = sqlc.narg(period_end),
    cancelled_at = CASE WHEN sqlc.arg(status)::varchar(50) = 'cancelled' THEN NOW() ELSE cancelled_at END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND cluster_id = sqlc.arg(cluster_id);

-- name: GetMarketplaceOwnerApproval :one
SELECT status, payout_eligible
FROM purser.cluster_owners
WHERE tenant_id = sqlc.arg(owner_id)::text::uuid;

-- name: GetExistingClusterPricing :one
SELECT pricing_model, metered_rates::text AS metered_rates
FROM purser.cluster_pricing
WHERE cluster_id = sqlc.arg(cluster_id);

-- name: UpsertClusterPricingConfig :one
INSERT INTO purser.cluster_pricing (
    cluster_id, pricing_model, base_price, currency,
    required_tier_level, allow_free_tier, metered_rates, default_quotas,
    stripe_product_id, stripe_price_id_monthly, stripe_meter_event_name,
    updated_at
) VALUES (
    sqlc.arg(cluster_id), sqlc.arg(pricing_model), sqlc.narg(base_price)::text::numeric,
    sqlc.arg(currency), sqlc.arg(required_tier_level), sqlc.arg(allow_free_tier),
    sqlc.arg(metered_rates), sqlc.arg(default_quotas),
    sqlc.narg(stripe_product_id), sqlc.narg(stripe_price_id_monthly),
    sqlc.narg(stripe_meter_event_name), NOW()
)
ON CONFLICT (cluster_id) DO UPDATE SET
    pricing_model = EXCLUDED.pricing_model,
    base_price = EXCLUDED.base_price,
    currency = EXCLUDED.currency,
    required_tier_level = EXCLUDED.required_tier_level,
    allow_free_tier = EXCLUDED.allow_free_tier,
    metered_rates = EXCLUDED.metered_rates,
    default_quotas = EXCLUDED.default_quotas,
    stripe_product_id = COALESCE(EXCLUDED.stripe_product_id, purser.cluster_pricing.stripe_product_id),
    stripe_price_id_monthly = COALESCE(EXCLUDED.stripe_price_id_monthly, purser.cluster_pricing.stripe_price_id_monthly),
    stripe_meter_event_name = COALESCE(EXCLUDED.stripe_meter_event_name, purser.cluster_pricing.stripe_meter_event_name),
    updated_at = NOW()
RETURNING id::text AS id;

-- name: CountMarketplaceClusterPricings :one
SELECT COUNT(*)
FROM purser.cluster_pricing
WHERE required_tier_level <= sqlc.arg(tier_level)::int;

-- name: ListMarketplaceClusterPricingPage :many
SELECT cluster_id, pricing_model, COALESCE(base_price::text, '')::text AS base_price, currency,
       required_tier_level, COALESCE(created_at, TIMESTAMP 'epoch') AS created_at
FROM purser.cluster_pricing
WHERE required_tier_level <= sqlc.arg(tier_level)::int
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (sqlc.arg(backward)::boolean AND (created_at, cluster_id) > (sqlc.narg(cursor_at)::timestamp, sqlc.arg(cursor_id)::text))
      OR (NOT sqlc.arg(backward)::boolean AND (created_at, cluster_id) < (sqlc.narg(cursor_at)::timestamp, sqlc.arg(cursor_id)::text))
  )
ORDER BY
    CASE WHEN sqlc.arg(backward)::boolean THEN created_at END ASC,
    CASE WHEN sqlc.arg(backward)::boolean THEN cluster_id END ASC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN created_at END DESC,
    CASE WHEN NOT sqlc.arg(backward)::boolean THEN cluster_id END DESC
LIMIT sqlc.arg(result_limit);
