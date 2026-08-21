-- name: LockTenantSubscriptionForPromotion :one
SELECT id::text AS id, billing_model, tier_id::text AS tier_id, payment_method,
       stripe_subscription_id, mollie_subscription_id, billing_email, billing_name,
       COALESCE(billing_address, '{}'::jsonb) AS billing_address
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE;

-- name: GetPostpaidPromotionTier :one
SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active
FROM purser.billing_tiers
WHERE (
    sqlc.arg(has_requested_tier)::boolean
    AND id = sqlc.arg(requested_tier_id)::text::uuid
) OR (
    NOT sqlc.arg(has_requested_tier)::boolean
    AND is_default_postpaid = true AND is_active = true
)
ORDER BY CASE WHEN id = NULLIF(sqlc.arg(requested_tier_id)::text, '')::uuid THEN 0 ELSE 1 END, id
LIMIT 1;

-- name: PromotePrepaidTenantSubscription :one
UPDATE purser.tenant_subscriptions
SET billing_model = 'postpaid', tier_id = sqlc.arg(tier_id)::text::uuid,
    status = 'active', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND billing_model = 'prepaid'
RETURNING id::text AS id;

-- name: GetCanonicalPromotedSubscription :one
SELECT subscription.id::text AS subscription_id, subscription.billing_model,
       subscription.tier_id::text AS tier_id, tier.tier_level, tier.tier_name,
       COALESCE(balance.balance_cents, 0)::bigint AS balance_cents
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
LEFT JOIN purser.prepaid_balances balance
  ON balance.tenant_id = subscription.tenant_id
 AND balance.currency = sqlc.arg(currency)
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: LockTenantSubscriptionForTierChange :one
SELECT subscription.tier_id::text AS tier_id, tier.tier_level,
       subscription.billing_model, subscription.billing_period_start,
       subscription.billing_period_end, subscription.stripe_current_period_end
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE OF subscription;

-- name: GetTierForBillingChange :one
SELECT tier_level, tier_name, is_default_prepaid, is_active
FROM purser.billing_tiers
WHERE id = sqlc.arg(tier_id)::text::uuid;

-- name: GetPostpaidCollectionSetup :one
SELECT payment_method, stripe_subscription_id, mollie_subscription_id,
       billing_email, billing_name, COALESCE(billing_address, '{}'::jsonb) AS billing_address
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetOpenInvoiceBillingPeriod :one
SELECT period_start, period_end
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start IS NOT NULL AND period_end IS NOT NULL
  AND status IN ('draft', 'manual_review')
  AND period_end > sqlc.arg(now)
ORDER BY period_end ASC
LIMIT 1;

-- name: BackfillTenantBillingPeriod :exec
UPDATE purser.tenant_subscriptions
SET billing_period_start = COALESCE(billing_period_start, sqlc.arg(period_start)),
    billing_period_end = COALESCE(billing_period_end, sqlc.arg(period_end)),
    next_billing_date = COALESCE(next_billing_date, sqlc.arg(period_end)),
    updated_at = CASE
        WHEN billing_period_start IS NULL OR billing_period_end IS NULL OR next_billing_date IS NULL
        THEN NOW() ELSE updated_at
    END
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ApplyTenantTierUpgrade :exec
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(tier_id)::text::uuid,
    pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL,
    billing_period_start = COALESCE(billing_period_start, sqlc.arg(period_start)),
    billing_period_end = COALESCE(billing_period_end, sqlc.arg(period_end)),
    next_billing_date = COALESCE(next_billing_date, sqlc.arg(period_end)),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ScheduleTenantTierDowngrade :exec
UPDATE purser.tenant_subscriptions
SET pending_tier_id = sqlc.arg(tier_id)::text::uuid,
    pending_effective_at = sqlc.arg(effective_at), pending_reason = 'downgrade',
    billing_period_start = COALESCE(billing_period_start, sqlc.arg(period_start)),
    billing_period_end = COALESCE(billing_period_end, sqlc.arg(period_end)),
    next_billing_date = COALESCE(next_billing_date, sqlc.arg(period_end)),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;
