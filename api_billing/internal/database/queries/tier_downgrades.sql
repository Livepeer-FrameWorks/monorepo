-- name: GetProviderTierTarget :one
-- The tier the recurring provider subscription must charge next: a scheduled
-- downgrade's target, otherwise the current tier.
SELECT target.id::text AS tier_id,
       subscription.tier_id::text AS current_tier_id,
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
       (subscription.free_fallback_at IS NOT NULL)::boolean AS fallback_scheduled,
       (subscription.status = 'active' AND subscription.free_fallback_at IS NOT NULL
        AND subscription.free_fallback_reason IS NOT DISTINCT FROM 'cancelled'
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

-- name: IsConfirmedDowngradeDue :one
-- The provider renews at the confirmed target from the effective time, while the
-- local flip waits for that period's invoice. That term is paid at the target's fee.
SELECT EXISTS (
    SELECT 1 FROM purser.tenant_subscriptions subscription
    WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND subscription.pending_reason = 'downgrade'
      AND subscription.pending_provider_tier_id = subscription.pending_tier_id
      AND subscription.pending_effective_at <= NOW()
)::boolean AS due;

-- name: HasUnfinishedProviderTierSync :one
SELECT EXISTS (
    SELECT 1 FROM purser.provider_webhook_inbox inbox
    WHERE inbox.provider = 'tier_downgrade'
      AND inbox.event_key = 'tier-downgrade:' || sqlc.arg(tenant_id)::text
      AND inbox.status <> 'processed'
)::boolean AS unfinished;

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

-- name: CountHeldConfirmedDowngrades :one
-- Service-wide alert inventory; tenant filtering would hide a held downgrade.
-- These customers are refused tier changes until an operator resolves the hold.
SELECT COUNT(*)::bigint AS items
FROM purser.tenant_subscriptions subscription
WHERE subscription.pending_tier_id IS NOT NULL
  AND subscription.pending_reason = 'downgrade'
  AND subscription.pending_provider_tier_id = subscription.pending_tier_id
  AND subscription.pending_effective_at < NOW() - (sqlc.arg(grace_seconds)::bigint * INTERVAL '1 second');

-- name: ListHeldTierDowngrades :many
-- The rows CountHeldConfirmedDowngrades counts, so recovery list names each
-- tenant behind PurserTierDowngradeHeld.
SELECT subscription.tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions subscription
WHERE subscription.pending_tier_id IS NOT NULL
  AND subscription.pending_reason = 'downgrade'
  AND subscription.pending_provider_tier_id = subscription.pending_tier_id
  AND subscription.pending_effective_at < NOW() - (sqlc.arg(grace_seconds)::bigint * INTERVAL '1 second')
  AND (sqlc.arg(tenant_id)::text = '' OR subscription.tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid)
ORDER BY subscription.pending_effective_at, subscription.tenant_id
LIMIT sqlc.arg(row_limit)::bigint;

-- name: GetTierDowngradeHold :one
-- A downgrade past its effective time that has not applied, with the invoice
-- closing the period it waits on. A finalized invoice sorts first because the
-- applier runs once one exists.
SELECT subscription.tenant_id::text AS tenant_id,
       target.id::text AS target_tier_id,
       target.tier_name::text AS target_tier_name,
       subscription.pending_effective_at::timestamptz AS effective_at,
       (subscription.pending_provider_tier_id IS NOT DISTINCT FROM subscription.pending_tier_id
        OR (NULLIF(subscription.stripe_subscription_id, '') IS NULL
            AND NULLIF(subscription.mollie_subscription_id, '') IS NULL))::boolean AS provider_confirmed,
       COALESCE(closing.id::text, '')::text AS invoice_id,
       COALESCE(closing.status, '')::text AS invoice_status
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers target ON target.id = subscription.pending_tier_id
LEFT JOIN LATERAL (
    SELECT invoice.id, invoice.status
    FROM purser.billing_invoices invoice
    WHERE invoice.tenant_id = subscription.tenant_id
      AND invoice.period_end = subscription.pending_effective_at
    ORDER BY (invoice.status IN ('draft', 'manual_review')), invoice.created_at DESC
    LIMIT 1
) closing ON TRUE
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND subscription.pending_reason = 'downgrade'
  AND subscription.pending_effective_at <= NOW();

-- name: GetSubscriptionDueForInvoice :one
-- ListSubscriptionsDueForInvoice for one tenant: the same predicates and
-- columns, so an operator's downgrade recovery closes exactly the period the
-- daily invoice job would close.
SELECT ts.tenant_id::text AS tenant_id,
       ts.billing_email,
       ts.tier_id,
       ts.status,
       ts.billing_period_start,
       ts.billing_period_end,
       ts.mollie_next_payment_date,
       ts.stripe_subscription_id,
       ts.mollie_subscription_id,
       ts.payment_method,
       ts.stripe_customer_id,
       ts.presentment_currency::text AS presentment_currency,
       EXISTS (
           SELECT 1 FROM purser.mollie_customers customer WHERE customer.tenant_id = ts.tenant_id
       )::boolean AS has_mollie_customer,
       bt.tier_name,
       bt.display_name,
       bt.billing_period,
       ts.billing_model
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id AND bt.is_active = TRUE
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ts.status = 'active' AND ts.pending_reason IS DISTINCT FROM 'tier_upgrade'
  AND ((ts.mollie_next_payment_date IS NOT NULL
            AND ts.mollie_next_payment_date <= (sqlc.arg(now)::timestamp)::date)
    OR (ts.billing_period_end IS NOT NULL
            AND ts.billing_period_end <= sqlc.arg(now)::timestamp)
    OR (ts.billing_period_end IS NULL
            AND (ts.next_billing_date IS NULL OR ts.next_billing_date <= sqlc.arg(now)::timestamp)))
  AND NOT EXISTS (
      SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = ts.tenant_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM purser.subscription_operator_grants grant_row
      WHERE grant_row.subscription_id = ts.id
        AND grant_row.expires_at <= sqlc.arg(now)::timestamptz
  );

-- name: IsPendingDowngradeDueForTenant :one
-- ListDuePendingDowngradeTenantIDs for one tenant: the applier runs only once
-- the invoice closing the downgrade's period is finalized.
SELECT EXISTS (
    SELECT 1
    FROM purser.tenant_subscriptions subscription
    WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND subscription.status = 'active'
      AND subscription.pending_tier_id IS NOT NULL
      AND subscription.pending_effective_at <= sqlc.arg(now)
      AND EXISTS (
          SELECT 1
          FROM purser.billing_invoices invoice
          WHERE invoice.tenant_id = subscription.tenant_id
            AND invoice.period_end = subscription.pending_effective_at
            AND invoice.status NOT IN ('draft', 'manual_review')
      )
)::boolean AS due;
