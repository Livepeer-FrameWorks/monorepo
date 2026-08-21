-- name: GetStripeTierCheckoutConfig :one
SELECT tier_name, COALESCE(currency, 'EUR')::text AS currency,
       COALESCE((CASE WHEN sqlc.arg(yearly)::boolean THEN stripe_price_id_yearly ELSE stripe_price_id_monthly END)::text, '')::text AS price_id
FROM purser.billing_tiers
WHERE id = sqlc.arg(tier_id)::text::uuid;

-- name: GetTenantPendingTierState :one
SELECT pending_reason, COALESCE(pending_tier_id::text, '')::text AS pending_tier_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: UpsertStripeTenantCheckoutIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, idempotency_key
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'stripe', 'tenant_subscription_checkout',
    'billing_tiers', sqlc.arg(tier_id)::text::uuid, 'pending',
    sqlc.arg(currency), sqlc.arg(idempotency_key)
)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET
    attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: SetProviderIntentCustomer :exec
UPDATE purser.payment_provider_intents
SET provider_customer_id = sqlc.arg(customer_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid
  AND (provider_customer_id IS NULL OR provider_customer_id = sqlc.arg(customer_id));

-- name: SetProviderIntentSessionOpen :exec
UPDATE purser.payment_provider_intents
SET provider_session_id = sqlc.arg(session_id), status = 'provider_open', updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: LinkProviderIntentCustomer :exec
UPDATE purser.payment_provider_intents
SET provider_customer_id = sqlc.arg(customer_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: StageStripeCheckoutTier :execrows
UPDATE purser.tenant_subscriptions
SET pending_tier_id = sqlc.arg(tier_id)::text::uuid,
    pending_reason = 'stripe_checkout', pending_effective_at = NULL,
    pending_intent_id = sqlc.arg(intent_id)::text::uuid, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (pending_tier_id IS NULL OR pending_reason = 'stripe_checkout');

-- name: GetTenantStripeCustomerID :one
SELECT stripe_customer_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetTenantStripeSubscriptionID :one
SELECT stripe_subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: UpdateTenantStripeSubscriptionSync :exec
UPDATE purser.tenant_subscriptions
SET stripe_subscription_status = sqlc.arg(status),
    stripe_current_period_end = sqlc.narg(period_end)::timestamptz,
    billing_period_start = COALESCE(sqlc.narg(period_start)::timestamp, billing_period_start),
    billing_period_end = COALESCE(sqlc.narg(period_end)::timestamp, billing_period_end),
    next_billing_date = COALESCE(sqlc.narg(period_end)::timestamp, next_billing_date),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetMollieTierPrice :one
SELECT tier_name, COALESCE(base_price::text, '0')::text AS base_price,
       COALESCE(currency, 'EUR')::text AS currency
FROM purser.billing_tiers
WHERE id = sqlc.arg(tier_id)::text::uuid;

-- name: UpsertMollieFirstPaymentIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, amount_cents, idempotency_key
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'mollie', 'mollie_first_payment',
    'billing_tiers', sqlc.arg(tier_id)::text::uuid, 'pending',
    sqlc.arg(currency), sqlc.arg(amount_cents), sqlc.arg(idempotency_key)
)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET
    attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: GetMollieCustomerID :one
SELECT mollie_customer_id
FROM purser.mollie_customers
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: SetProviderIntentPaymentOpen :exec
UPDATE purser.payment_provider_intents
SET provider_payment_id = sqlc.arg(payment_id), status = 'provider_open', updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: TenantSubscriptionExists :one
SELECT EXISTS(
    SELECT 1 FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
);

-- name: UpsertMollieSubscriptionIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    provider_customer_id, status, currency, amount_cents, idempotency_key
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'mollie', 'mollie_subscription_create',
    'billing_tiers', sqlc.arg(tier_id)::text::uuid, sqlc.arg(customer_id),
    'pending', sqlc.arg(currency), sqlc.arg(amount_cents), sqlc.arg(idempotency_key)
)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET
    attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: SetProviderIntentSubscriptionOpen :exec
UPDATE purser.payment_provider_intents
SET provider_subscription_id = sqlc.arg(subscription_id), status = 'provider_open', updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: ActivateMollieTenantSubscription :execrows
UPDATE purser.tenant_subscriptions
SET mollie_subscription_id = sqlc.arg(subscription_id),
    mollie_next_payment_date = sqlc.arg(next_payment_date)::date,
    next_billing_date = sqlc.arg(next_payment_date)::timestamp,
    billing_period_start = sqlc.arg(period_start),
    billing_period_end = sqlc.arg(period_end),
    tier_id = sqlc.arg(tier_id)::text::uuid,
    payment_method = 'mollie', status = 'active', billing_model = 'postpaid',
    pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetTenantMollieSubscriptionID :one
SELECT mollie_subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CancelLocalMollieSubscription :exec
UPDATE purser.tenant_subscriptions
SET mollie_subscription_id = NULL, status = 'cancelled',
    cancelled_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;
