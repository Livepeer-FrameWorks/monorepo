-- name: SelfServiceAdvanceTierChange :one
-- Paid advance changes need the durable upgrade boundary instead of another setup.
SELECT EXISTS (
    SELECT 1 FROM purser.tenant_subscriptions subscription
    JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
    WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND subscription.billing_model = 'postpaid'
      AND NULLIF(subscription.stripe_subscription_id, '') IS NULL
      AND NULLIF(subscription.mollie_subscription_id, '') IS NULL
      AND tier.base_price > 0
      AND subscription.billing_period_start < NOW() AND subscription.billing_period_end > NOW()
      AND subscription.tier_id <> sqlc.arg(tier_id)::text::uuid
)::boolean AS changing;

-- name: GetSelfServiceCheckoutTier :one
SELECT COALESCE(local_reference_id::text, '')::text AS tier_id
FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND id = sqlc.arg(intent_id)::text::uuid
  AND purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create');

-- name: GetSelfServiceNativeSubscriptions :one
SELECT CASE
           WHEN free_fallback_provider_ended_at IS NULL
            AND COALESCE(stripe_subscription_status, '') NOT IN ('canceled', 'incomplete_expired')
           THEN COALESCE(stripe_subscription_id, '') ELSE ''
       END::text AS stripe_id,
       CASE WHEN free_fallback_provider_ended_at IS NULL
            THEN COALESCE(mollie_subscription_id, '') ELSE ''
       END::text AS mollie_id,
       (free_fallback_provider_ended_at IS NOT NULL AND status = 'pending_cancellation')::boolean AS paid_cancellation_term
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;
