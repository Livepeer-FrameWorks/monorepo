-- name: MarkStripeIntentSucceededBySession :exec
UPDATE purser.payment_provider_intents
SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF(sqlc.arg(subscription_id)::text, '')),
    status = 'succeeded', succeeded_at = COALESCE(succeeded_at, NOW()), updated_at = NOW()
WHERE provider = 'stripe' AND provider_session_id = sqlc.arg(session_id)::text;

-- name: ActivateTenantSubscriptionFromStripe :execrows
UPDATE purser.tenant_subscriptions
SET stripe_customer_id = COALESCE(NULLIF(sqlc.arg(customer_id)::text, ''), stripe_customer_id),
    stripe_subscription_id = COALESCE(NULLIF(sqlc.arg(subscription_id)::text, ''), stripe_subscription_id),
    stripe_subscription_status = 'active', status = 'active', billing_model = 'postpaid',
    tier_id = COALESCE(
        NULLIF(sqlc.arg(tier_id)::text, '')::uuid,
        CASE WHEN pending_reason = 'stripe_checkout' THEN pending_tier_id END,
        tier_id
    ),
    payment_method = 'stripe',
    pending_tier_id = CASE
        WHEN pending_reason = 'stripe_checkout'
         AND (NULLIF(sqlc.arg(tier_id)::text, '') IS NULL OR pending_tier_id = NULLIF(sqlc.arg(tier_id)::text, '')::uuid)
        THEN NULL ELSE pending_tier_id END,
    pending_effective_at = CASE
        WHEN pending_reason = 'stripe_checkout'
         AND (NULLIF(sqlc.arg(tier_id)::text, '') IS NULL OR pending_tier_id = NULLIF(sqlc.arg(tier_id)::text, '')::uuid)
        THEN NULL ELSE pending_effective_at END,
    pending_reason = CASE
        WHEN pending_reason = 'stripe_checkout'
         AND (NULLIF(sqlc.arg(tier_id)::text, '') IS NULL OR pending_tier_id = NULLIF(sqlc.arg(tier_id)::text, '')::uuid)
        THEN NULL ELSE pending_reason END,
    pending_intent_id = CASE
        WHEN pending_reason = 'stripe_checkout'
         AND (NULLIF(sqlc.arg(tier_id)::text, '') IS NULL OR pending_tier_id = NULLIF(sqlc.arg(tier_id)::text, '')::uuid)
        THEN NULL ELSE pending_intent_id END,
    stripe_current_period_end = COALESCE(sqlc.narg(period_end), stripe_current_period_end),
    billing_period_start = COALESCE(sqlc.narg(period_start), billing_period_start),
    billing_period_end = COALESCE(sqlc.narg(period_end), billing_period_end),
    next_billing_date = COALESCE(sqlc.narg(period_end), next_billing_date),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: StageTenantSubscriptionPendingStripe :exec
UPDATE purser.tenant_subscriptions
SET stripe_customer_id = COALESCE(NULLIF(sqlc.arg(customer_id)::text, ''), stripe_customer_id),
    stripe_subscription_id = COALESCE(NULLIF(sqlc.arg(subscription_id)::text, ''), stripe_subscription_id),
    stripe_subscription_status = CASE WHEN status = 'active' THEN stripe_subscription_status ELSE 'incomplete' END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ResolveClusterSubscriptionByStripeID :one
SELECT tenant_id::text AS tenant_id, cluster_id,
       COALESCE(stripe_customer_id, '')::text AS stripe_customer_id
FROM purser.cluster_subscriptions
WHERE stripe_subscription_id = sqlc.arg(subscription_id)::text;

-- name: GetClusterSubscriptionStatus :one
SELECT status
FROM purser.cluster_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND cluster_id = sqlc.arg(cluster_id);

-- name: UpsertActiveStripeClusterSubscription :exec
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, stripe_customer_id, stripe_subscription_id,
    stripe_subscription_status, checkout_session_id, intent_id, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), 'active',
    NULLIF(sqlc.arg(customer_id)::text, ''), NULLIF(sqlc.arg(subscription_id)::text, ''),
    'active', NULLIF(sqlc.arg(session_id)::text, ''),
    (SELECT id FROM purser.payment_provider_intents
     WHERE provider = 'stripe' AND provider_session_id = sqlc.arg(session_id)::text LIMIT 1),
    NOW(), NOW()
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    status = 'active',
    stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, purser.cluster_subscriptions.stripe_customer_id),
    stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, purser.cluster_subscriptions.stripe_subscription_id),
    stripe_subscription_status = 'active',
    checkout_session_id = COALESCE(EXCLUDED.checkout_session_id, purser.cluster_subscriptions.checkout_session_id),
    intent_id = COALESCE(purser.cluster_subscriptions.intent_id, EXCLUDED.intent_id),
    updated_at = NOW();

-- name: MarkStripeClusterIntentSucceeded :exec
UPDATE purser.payment_provider_intents
SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF(sqlc.arg(subscription_id)::text, '')),
    status = 'succeeded', succeeded_at = COALESCE(succeeded_at, NOW()), updated_at = NOW()
WHERE provider = 'stripe'
  AND (provider_session_id = NULLIF(sqlc.arg(session_id)::text, '')
       OR provider_subscription_id = NULLIF(sqlc.arg(subscription_id)::text, ''));

-- name: UpsertPendingStripeClusterSubscription :exec
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, stripe_customer_id, stripe_subscription_id,
    stripe_subscription_status, checkout_session_id, intent_id, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), 'pending_payment',
    NULLIF(sqlc.arg(customer_id)::text, ''), NULLIF(sqlc.arg(subscription_id)::text, ''),
    'incomplete', NULLIF(sqlc.arg(session_id)::text, ''),
    (SELECT id FROM purser.payment_provider_intents
     WHERE provider = 'stripe' AND provider_session_id = sqlc.arg(session_id)::text LIMIT 1),
    NOW(), NOW()
)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, purser.cluster_subscriptions.stripe_customer_id),
    stripe_subscription_id = COALESCE(EXCLUDED.stripe_subscription_id, purser.cluster_subscriptions.stripe_subscription_id),
    stripe_subscription_status = CASE
        WHEN purser.cluster_subscriptions.status = 'active'
        THEN purser.cluster_subscriptions.stripe_subscription_status ELSE 'incomplete' END,
    checkout_session_id = COALESCE(EXCLUDED.checkout_session_id, purser.cluster_subscriptions.checkout_session_id),
    intent_id = COALESCE(purser.cluster_subscriptions.intent_id, EXCLUDED.intent_id),
    updated_at = NOW();

-- name: ExpireStripeIntentBySubscription :exec
UPDATE purser.payment_provider_intents
SET status = 'expired', updated_at = NOW()
WHERE provider = 'stripe' AND provider_subscription_id = sqlc.arg(subscription_id)::text
  AND status NOT IN ('succeeded', 'cancelled', 'expired', 'terminal_failed');

-- name: ClearTenantStripeCheckoutPending :exec
UPDATE purser.tenant_subscriptions
SET pending_tier_id = NULL, pending_effective_at = NULL,
    pending_reason = NULL, pending_intent_id = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND pending_reason = 'stripe_checkout';

-- name: MarkPendingTopupTerminal :exec
UPDATE purser.pending_topups
SET status = sqlc.arg(status), updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid AND status = 'pending';

-- name: LinkStripeIntentSubscription :exec
UPDATE purser.payment_provider_intents
SET provider_subscription_id = COALESCE(provider_subscription_id, sqlc.arg(subscription_id)::text), updated_at = NOW()
WHERE provider = 'stripe' AND provider_session_id = sqlc.arg(session_id)::text;

-- name: ExpireStripeIntentBySession :exec
UPDATE purser.payment_provider_intents
SET status = 'expired', updated_at = NOW()
WHERE provider = 'stripe' AND provider_session_id = sqlc.arg(session_id)::text
  AND status NOT IN ('succeeded', 'cancelled', 'expired', 'terminal_failed');

-- name: ClearStagedStripeClusterSubscription :exec
UPDATE purser.cluster_subscriptions
SET status = 'cancelled', stripe_subscription_status = 'incomplete_expired', updated_at = NOW()
WHERE status = 'pending_payment'
  AND (checkout_session_id = NULLIF(sqlc.arg(session_id)::text, '')
       OR stripe_subscription_id = NULLIF(sqlc.arg(subscription_id)::text, ''));

-- name: AttachStripeIntentToInvoicePayment :exec
UPDATE purser.billing_payments
SET tx_id = sqlc.arg(payment_intent_id)::text, updated_at = NOW()
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND method = 'card' AND status = 'pending' AND tx_id = sqlc.arg(session_id)::text;

-- name: LockPendingTopupForCheckout :one
SELECT status, tenant_id::text AS tenant_id
FROM purser.pending_topups
WHERE id = sqlc.arg(topup_id)::text::uuid
FOR UPDATE;

-- name: AttachProviderPaymentToPendingTopup :exec
UPDATE purser.pending_topups
SET provider_payment_id = COALESCE(provider_payment_id, NULLIF(sqlc.arg(provider_payment_id)::text, '')),
    checkout_id = COALESCE(checkout_id, NULLIF(sqlc.arg(session_id)::text, '')), updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid;

-- name: CompletePendingTopup :exec
UPDATE purser.pending_topups
SET status = 'completed', completed_at = sqlc.arg(completed_at),
    balance_transaction_id = sqlc.arg(balance_transaction_id)::text::uuid, updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid;

-- name: CompletePendingTopupProviderIntent :exec
UPDATE purser.payment_provider_intents intent
SET provider_payment_id = COALESCE(intent.provider_payment_id, NULLIF(sqlc.arg(provider_payment_id)::text, '')),
    provider_session_id = COALESCE(intent.provider_session_id, NULLIF(sqlc.arg(session_id)::text, '')),
    status = 'succeeded', succeeded_at = COALESCE(intent.succeeded_at, sqlc.arg(completed_at)), updated_at = NOW()
FROM purser.pending_topups topup
WHERE topup.intent_id = intent.id AND topup.id = sqlc.arg(topup_id)::text::uuid;
