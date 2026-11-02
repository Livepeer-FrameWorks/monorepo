-- name: GetProviderTierTarget :one
-- The tier the recurring provider subscription must charge next: a scheduled
-- downgrade's target, otherwise the current tier.
SELECT target.id::text AS tier_id,
       (subscription.pending_reason IS NOT DISTINCT FROM 'downgrade')::boolean AS downgrade_pending,
       COALESCE(NULLIF(subscription.stripe_subscription_id, ''), '')::text AS stripe_subscription_id,
       COALESCE(NULLIF(subscription.mollie_subscription_id, ''), '')::text AS mollie_subscription_id,
       COALESCE(subscription.stripe_customer_id, '')::text AS stripe_customer_id,
       COALESCE(customer.mollie_customer_id, '')::text AS mollie_customer_id,
       COALESCE(target.stripe_price_id_monthly, '')::text AS stripe_price_id,
       ROUND(target.base_price * 100)::bigint AS base_cents,
       COALESCE(target.currency, 'EUR')::text AS currency,
       (target.base_price = 0 AND COALESCE(target.is_default_postpaid, false))::boolean AS target_is_free,
       subscription.pending_effective_at,
       (subscription.status = 'active' AND subscription.free_fallback_at IS NOT NULL
        AND subscription.free_fallback_provider_ended_at IS NULL)::boolean AS provider_stop_scheduled
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers target
  ON target.id = CASE WHEN subscription.pending_reason = 'downgrade' THEN subscription.pending_tier_id ELSE subscription.tier_id END
LEFT JOIN purser.mollie_customers customer ON customer.tenant_id = subscription.tenant_id
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: EnqueueProviderTierSync :exec
-- One row per tenant: the worker reads the latest desired tier, so a re-point
-- wakes the same step instead of queueing a stale target.
INSERT INTO purser.provider_webhook_inbox AS inbox (provider, event_key, headers, raw_payload)
VALUES ('tier_downgrade', 'tier-downgrade:' || sqlc.arg(tenant_id)::text,
        jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text),
        convert_to(jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text)::text, 'UTF8'))
ON CONFLICT (provider, event_key) DO UPDATE SET
    status = CASE WHEN inbox.status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE, next_attempt_at = NOW(), processed_at = NULL,
    attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.attempts END,
    wait_attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.wait_attempts END,
    unresolved_since = CASE WHEN inbox.status = 'processed' THEN NULL ELSE inbox.unresolved_since END,
    updated_at = NOW();

-- name: EnqueueUnconfirmedTierDowngrades :exec
-- Service-wide discovery gives every unconfirmed downgrade durable work. A move
-- to Free has nothing to do until its provider-stop lead begins, which is what
-- wakes it. Tenant filtering would strand those subscriptions.
INSERT INTO purser.provider_webhook_inbox AS inbox (provider, event_key, headers, raw_payload)
SELECT 'tier_downgrade', 'tier-downgrade:' || subscription.tenant_id::text,
       jsonb_build_object('tenant_id', subscription.tenant_id::text),
       convert_to(jsonb_build_object('tenant_id', subscription.tenant_id::text)::text, 'UTF8')
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers target ON target.id = subscription.pending_tier_id
WHERE subscription.pending_tier_id IS NOT NULL
  AND subscription.pending_reason = 'downgrade'
  AND subscription.pending_provider_tier_id IS DISTINCT FROM subscription.pending_tier_id
  AND (NULLIF(subscription.stripe_subscription_id, '') IS NOT NULL OR NULLIF(subscription.mollie_subscription_id, '') IS NOT NULL)
  AND NOT subscription.free_fallback_provider_stopping
  AND subscription.free_fallback_provider_ended_at IS NULL
  AND subscription.free_fallback_at IS NULL
  AND (target.base_price > 0 OR NOT COALESCE(target.is_default_postpaid, false)
       OR subscription.pending_effective_at <= NOW() + (sqlc.arg(free_stop_lead_seconds)::bigint * INTERVAL '1 second'))
ON CONFLICT (provider, event_key) DO UPDATE SET
    status = 'pending', next_attempt_at = NOW(), processed_at = NULL,
    attempts = 0, wait_attempts = 0, unresolved_since = NULL, updated_at = NOW()
WHERE inbox.status = 'processed';

-- name: ConfirmProviderTier :exec
-- The provider's paid term can already extend past a late confirmation; that
-- term keeps its tier, so the effective time never precedes it.
UPDATE purser.tenant_subscriptions
SET pending_provider_tier_id = CASE
        WHEN pending_reason = 'downgrade' AND pending_tier_id = sqlc.arg(tier_id)::text::uuid
        THEN pending_tier_id END,
    pending_effective_at = CASE
        WHEN pending_reason = 'downgrade'
        THEN GREATEST(pending_effective_at, sqlc.narg(paid_through)::timestamptz)
        ELSE pending_effective_at END,
    provider_tier_changed_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CancelTenantTierDowngrade :execrows
UPDATE purser.tenant_subscriptions
SET pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL,
    pending_provider_tier_id = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND pending_reason = 'downgrade';

-- name: GetStripeRenewalTierFence :one
SELECT (subscription.pending_reason IS NOT DISTINCT FROM 'downgrade')::boolean AS downgrade_pending,
       subscription.provider_tier_changed_at,
       EXISTS (
           SELECT 1 FROM purser.provider_webhook_inbox inbox
           WHERE inbox.provider = 'tier_downgrade'
             AND inbox.event_key = 'tier-downgrade:' || subscription.tenant_id::text
             AND inbox.status <> 'processed'
       )::boolean AS sync_unfinished
FROM purser.tenant_subscriptions subscription
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid;
