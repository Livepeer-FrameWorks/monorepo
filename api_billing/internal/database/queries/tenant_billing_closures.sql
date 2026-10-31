-- name: RecordTenantBillingClosure :exec
INSERT INTO purser.tenant_billing_closures (
    tenant_id, closed_at, source_event_id, provider_subscriptions
)
SELECT sqlc.arg(tenant_id)::text::uuid, sqlc.arg(closed_at)::timestamptz,
       sqlc.arg(event_id)::text,
       COALESCE((
           SELECT jsonb_agg(jsonb_build_object(
               'provider', refs.provider, 'customer_id', refs.customer_id,
               'subscription_id', refs.subscription_id, 'established', refs.established
           ))
           FROM (
               SELECT 'stripe' AS provider, COALESCE(stripe_customer_id, '') AS customer_id,
                      stripe_subscription_id AS subscription_id, true AS established
               FROM purser.tenant_subscriptions
               WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
                 AND NULLIF(stripe_subscription_id, '') IS NOT NULL
               UNION
               SELECT 'mollie', COALESCE((
                   SELECT mollie_customer_id FROM purser.mollie_customers
                   WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
               ), ''), mollie_subscription_id, true
               FROM purser.tenant_subscriptions
               WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
                 AND NULLIF(mollie_subscription_id, '') IS NOT NULL
               UNION
               SELECT 'stripe', COALESCE(stripe_customer_id, ''), stripe_subscription_id,
                      status IN ('active', 'cancelled')
               FROM purser.cluster_subscriptions
               WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
                 AND NULLIF(stripe_subscription_id, '') IS NOT NULL
           ) refs
       ), '[]'::jsonb)
ON CONFLICT (tenant_id) DO NOTHING;

-- name: GetTenantBillingClosure :one
SELECT closed_at, provider_subscriptions, providers_cancelled_at, finalized_at
FROM purser.tenant_billing_closures
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: EnqueueTenantBillingClosure :exec
INSERT INTO purser.provider_webhook_inbox (
    provider, event_key, headers, raw_payload, next_attempt_at
)
SELECT 'tenant_closure', 'tenant-closure:' || tenant_id::text,
       jsonb_build_object('tenant_id', tenant_id::text),
       convert_to(jsonb_build_object('tenant_id', tenant_id::text)::text, 'UTF8'), NOW()
FROM purser.tenant_billing_closures
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND finalized_at IS NULL
ON CONFLICT (provider, event_key) DO NOTHING;

-- name: EndClosedTenantGrantAtCutoff :exec
UPDATE purser.subscription_operator_grants g
SET expires_at = LEAST(COALESCE(g.expires_at, closure.closed_at), closure.closed_at)
FROM purser.tenant_subscriptions ts
JOIN purser.tenant_billing_closures closure ON closure.tenant_id = ts.tenant_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND g.subscription_id = ts.id;

-- name: CompensateClosedTenantCheckouts :many
UPDATE purser.payment_provider_intents intent
SET recovery_started_at = COALESCE(recovery_started_at, NOW()), updated_at = NOW()
WHERE intent.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND intent.purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create', 'cluster_subscription_checkout')
  AND (intent.status IN ('pending', 'provider_open', 'provider_call_failed')
       OR (intent.purpose = 'mollie_first_payment' AND intent.status = 'succeeded'
           AND intent.activated_at IS NULL))
  AND NOT EXISTS (
      SELECT 1 FROM purser.tenant_subscriptions ts
      WHERE ts.tenant_id = intent.tenant_id
        AND ((intent.provider = 'stripe' AND intent.provider_subscription_id = ts.stripe_subscription_id)
          OR (intent.provider = 'mollie' AND intent.provider_subscription_id = ts.mollie_subscription_id))
  )
  AND NOT EXISTS (
      SELECT 1 FROM purser.tenant_billing_closures closure,
           jsonb_array_elements(closure.provider_subscriptions) ref
      WHERE closure.tenant_id = intent.tenant_id
        AND ref->>'provider' = intent.provider
        AND ref->>'subscription_id' = intent.provider_subscription_id
        AND (ref->>'established')::boolean
  )
RETURNING intent.id::text;

-- name: AppendClosedTenantStripeSubscription :execrows
UPDATE purser.tenant_billing_closures
SET provider_subscriptions = provider_subscriptions || jsonb_build_array(jsonb_build_object(
        'provider', 'stripe', 'customer_id', sqlc.arg(customer_id)::text,
        'subscription_id', sqlc.arg(subscription_id)::text
    )), providers_cancelled_at = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(provider_subscriptions) ref
      WHERE ref->>'provider' = 'stripe'
        AND ref->>'subscription_id' = sqlc.arg(subscription_id)::text
  );

-- name: RequeueTenantBillingClosure :exec
INSERT INTO purser.provider_webhook_inbox (
    provider, event_key, headers, raw_payload, next_attempt_at
)
SELECT 'tenant_closure', 'tenant-closure:' || tenant_id::text,
       jsonb_build_object('tenant_id', tenant_id::text),
       convert_to(jsonb_build_object('tenant_id', tenant_id::text)::text, 'UTF8'), NOW()
FROM purser.tenant_billing_closures
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND providers_cancelled_at IS NULL
ON CONFLICT (provider, event_key) DO UPDATE SET
    status = CASE WHEN purser.provider_webhook_inbox.status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE,
    next_attempt_at = LEAST(purser.provider_webhook_inbox.next_attempt_at, NOW()),
    attempts = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN 0 ELSE purser.provider_webhook_inbox.attempts END,
    wait_attempts = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN 0 ELSE purser.provider_webhook_inbox.wait_attempts END,
    unresolved_since = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN NULL ELSE purser.provider_webhook_inbox.unresolved_since END,
    last_error = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN NULL ELSE purser.provider_webhook_inbox.last_error END,
    processed_at = NULL, updated_at = NOW();

-- name: MarkClosedTenantProvidersCancelled :execrows
UPDATE purser.tenant_billing_closures
SET providers_cancelled_at = COALESCE(providers_cancelled_at, NOW())
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND provider_subscriptions = sqlc.arg(expected_references)::jsonb;

-- name: CompleteTenantBillingClosure :exec
UPDATE purser.tenant_billing_closures
SET finalized_at = COALESCE(finalized_at, NOW())
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CancelClosedTenantSubscription :exec
-- A phase opened after a delayed closure event has no interval before its cutoff.
UPDATE purser.tenant_subscriptions
SET status = 'cancelled', cancelled_at = sqlc.arg(closed_at)::timestamptz,
    next_billing_date = NULL, pending_tier_id = NULL, pending_effective_at = NULL,
    pending_reason = NULL, pending_intent_id = NULL,
    billing_period_start = LEAST(billing_period_start, sqlc.arg(closed_at)::timestamp),
    billing_period_end = sqlc.arg(closed_at)::timestamp, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CancelClosedTenantClusterSubscriptions :exec
UPDATE purser.cluster_subscriptions
SET status = 'cancelled', cancelled_at = LEAST(COALESCE(cancelled_at, sqlc.arg(closed_at)::timestamptz), sqlc.arg(closed_at)::timestamptz),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: HaltClosedTenantBilling :exec
UPDATE purser.tenant_subscriptions
SET free_fallback_at = NULL, free_fallback_reason = NULL, free_fallback_notified_at = NULL,
    free_fallback_moved_at = NULL, free_fallback_provider_stopping = FALSE, free_fallback_wait_since = NULL,
    pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL,
    pending_intent_id = NULL, next_billing_date = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: IsTenantBillingClosed :one
-- Call after acquiring the subscription row lock, so a lock wait cannot retain
-- a snapshot from before the account-closure marker committed.
SELECT EXISTS (
    SELECT 1 FROM purser.tenant_billing_closures
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
)::boolean AS closed;

-- name: ListClosedTenantUncollectedInvoices :many
-- Closure forbids new invoices, but issued debt still needs a collection attempt.
SELECT invoice.id::text, invoice.presentment_amount_cents,
       COALESCE(invoice.presentment_currency, invoice.currency)::text AS currency,
       subscription.payment_method::text AS provider
FROM purser.billing_invoices invoice
JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
JOIN purser.tenant_billing_closures closure ON closure.tenant_id = invoice.tenant_id
WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND subscription.payment_method IN ('stripe', 'mollie')
  AND invoice.status IN ('pending', 'overdue') AND invoice.finalized_at IS NOT NULL
  AND invoice.presentment_amount_cents > 0
  AND NOT EXISTS (
      SELECT 1 FROM purser.billing_payments payment WHERE payment.invoice_id = invoice.id
  )
ORDER BY invoice.created_at, invoice.id;
