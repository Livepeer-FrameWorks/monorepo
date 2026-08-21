-- name: ActiveBillingTierExists :one
SELECT EXISTS (
    SELECT 1 FROM purser.billing_tiers
    WHERE id = sqlc.arg(tier_id)::text::uuid AND COALESCE(is_active, true)
);

-- name: GetCurrentTenantSubscription :one
SELECT id, tenant_id, tier_id, status, billing_email, started_at,
       trial_ends_at, next_billing_date, cancelled_at,
       billing_period_start, billing_period_end,
       payment_method, payment_reference, tax_id, tax_rate,
       billing_model, COALESCE(custom_features::text, '')::text AS custom_features_text,
       COALESCE(billing_address::text, '')::text AS billing_address_text,
       stripe_customer_id, stripe_subscription_id, stripe_subscription_status,
       stripe_current_period_end, dunning_attempts, mollie_subscription_id,
       pending_tier_id, pending_effective_at, pending_reason,
       COALESCE(created_at, TIMESTAMP 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;

-- name: InsertTenantSubscription :exec
INSERT INTO purser.tenant_subscriptions (
    id, tenant_id, tier_id, status, billing_email, billing_model, started_at,
    trial_ends_at, next_billing_date, billing_period_start, billing_period_end,
    payment_method, custom_features, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(tier_id)::text::uuid,
    'active', sqlc.arg(billing_email), sqlc.arg(billing_model), sqlc.arg(now),
    sqlc.narg(trial_ends_at), sqlc.arg(next_billing_date),
    sqlc.arg(billing_period_start), sqlc.arg(billing_period_end),
    sqlc.arg(payment_method)::text, sqlc.arg(custom_features)::jsonb,
    sqlc.arg(now), sqlc.arg(now)
);

-- name: GetTenantSubscriptionTierID :one
SELECT tier_id::text AS tier_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: UpdateTenantSubscriptionFields :execrows
UPDATE purser.tenant_subscriptions
SET tier_id = CASE WHEN sqlc.arg(set_tier_id)::boolean THEN sqlc.arg(tier_id)::text::uuid ELSE tier_id END,
    billing_email = CASE WHEN sqlc.arg(set_billing_email)::boolean THEN sqlc.arg(billing_email)::text ELSE billing_email END,
    payment_method = CASE WHEN sqlc.arg(set_payment_method)::boolean THEN sqlc.arg(payment_method)::text ELSE payment_method END,
    status = CASE WHEN sqlc.arg(set_status)::boolean THEN sqlc.arg(status)::text ELSE status END,
    billing_period_start = CASE WHEN sqlc.arg(set_billing_period_start)::boolean THEN sqlc.arg(billing_period_start) ELSE billing_period_start END,
    billing_period_end = CASE WHEN sqlc.arg(set_billing_period_end)::boolean THEN sqlc.arg(billing_period_end) ELSE billing_period_end END,
    custom_features = CASE WHEN sqlc.arg(set_custom_features)::boolean THEN sqlc.arg(custom_features)::jsonb ELSE custom_features END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled';

-- name: GetUpdatedSubscriptionEventState :one
SELECT id, status, COALESCE(payment_method, '') AS payment_method
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: GetActiveSubscriptionID :one
SELECT id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: DeleteSubscriptionPricingOverrides :exec
DELETE FROM purser.subscription_pricing_overrides
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: UpsertSubscriptionPricingOverride :exec
INSERT INTO purser.subscription_pricing_overrides (
    subscription_id, meter, model, currency, included_quantity, unit_price, config
) VALUES (
    sqlc.arg(subscription_id), sqlc.arg(meter), NULLIF(sqlc.arg(model)::text, ''),
    NULLIF(sqlc.arg(currency)::text, ''), NULLIF(sqlc.arg(included_quantity)::text, '')::numeric,
    NULLIF(sqlc.arg(unit_price)::text, '')::numeric,
    COALESCE(NULLIF(sqlc.arg(config)::text, ''), '{}')::jsonb
)
ON CONFLICT (subscription_id, meter) DO UPDATE SET
    model = EXCLUDED.model,
    currency = EXCLUDED.currency,
    included_quantity = EXCLUDED.included_quantity,
    unit_price = EXCLUDED.unit_price,
    config = EXCLUDED.config;

-- name: DeleteSubscriptionEntitlementOverrides :exec
DELETE FROM purser.subscription_entitlement_overrides
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: UpsertSubscriptionEntitlementOverride :exec
INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
VALUES (sqlc.arg(subscription_id), sqlc.arg(key), sqlc.arg(value)::jsonb)
ON CONFLICT (subscription_id, key) DO UPDATE SET value = EXCLUDED.value;

-- name: GetCancelableSubscriptionID :one
SELECT id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: RecordAccountClosureCollectionWriteoffs :execrows
INSERT INTO purser.billing_collection_writeoffs (tenant_id, currency, amount_cents, reason)
SELECT tenant_id, currency, balance_cents, 'account_closed'
FROM purser.billing_collection_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND balance_cents > 0;

-- name: ClearAccountClosureCollectionBalances :execrows
UPDATE purser.billing_collection_balances
SET balance_cents = 0, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND balance_cents > 0;

-- name: CancelTenantSubscriptions :execrows
UPDATE purser.tenant_subscriptions
SET status = 'cancelled', cancelled_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled';

-- name: GetDefaultBillingTier :one
SELECT id, COALESCE(tier_level, 0)::integer AS tier_level, tier_name
FROM purser.billing_tiers
WHERE (
    (sqlc.arg(prepaid)::boolean AND COALESCE(is_default_prepaid, false))
    OR (NOT sqlc.arg(prepaid)::boolean AND COALESCE(is_default_postpaid, false))
)
  AND COALESCE(is_active, true)
ORDER BY tier_level, id
LIMIT 1;

-- name: EnsureDefaultTenantSubscription :execrows
INSERT INTO purser.tenant_subscriptions (
    id, tenant_id, tier_id, billing_model, status, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(tier_id),
    sqlc.arg(billing_model), 'active', sqlc.arg(now), sqlc.arg(now)
)
ON CONFLICT (tenant_id) DO NOTHING;

-- name: EnsureRuntimePrepaidBalance :execrows
INSERT INTO purser.prepaid_balances (
    id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, 0, sqlc.arg(currency), 500,
    sqlc.arg(now), sqlc.arg(now)
)
ON CONFLICT (tenant_id, currency) DO NOTHING;

-- name: GetInitializedPrepaidAccount :one
SELECT ts.id AS subscription_id, pb.id AS balance_id,
       COALESCE(bt.tier_level, 0)::integer AS tier_level, bt.tier_name
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
JOIN purser.prepaid_balances pb
  ON pb.tenant_id = ts.tenant_id AND pb.currency = sqlc.arg(currency)
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetInitializedPostpaidAccount :one
SELECT ts.id AS subscription_id, COALESCE(bt.tier_level, 0)::integer AS tier_level,
       bt.tier_name
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetCanonicalSubscriptionTier :one
SELECT ts.tier_id::text AS tier_id, COALESCE(bt.tier_level, 0)::integer AS tier_level, bt.tier_name
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid;
