-- name: GetSubscriptionOperatorGrant :one
-- The operator grant recorded for a tenant's subscription, whether or not it
-- is still in force; active reports whether it applies now.
SELECT g.subscription_id::text AS subscription_id,
       g.base_price,
       g.waive_usage, g.collection, g.expires_at, g.reason, g.granted_by, g.granted_at,
       (g.expires_at IS NULL OR g.expires_at > NOW())::boolean AS active
FROM purser.tenant_subscriptions ts
JOIN purser.subscription_operator_grants g ON g.subscription_id = ts.id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: UpsertSubscriptionOperatorGrant :exec
INSERT INTO purser.subscription_operator_grants (
    subscription_id, base_price, waive_usage, collection, expires_at, reason, granted_by, granted_at
) VALUES (
    sqlc.arg(subscription_id)::text::uuid,
    sqlc.narg(base_price)::text::numeric,
    sqlc.arg(waive_usage),
    sqlc.arg(collection),
    sqlc.narg(expires_at),
    sqlc.arg(reason),
    NULLIF(sqlc.arg(granted_by)::text, ''),
    NOW()
)
ON CONFLICT (subscription_id) DO UPDATE SET
    base_price = EXCLUDED.base_price,
    waive_usage = EXCLUDED.waive_usage,
    collection = EXCLUDED.collection,
    expires_at = EXCLUDED.expires_at,
    reason = EXCLUDED.reason,
    granted_by = EXCLUDED.granted_by,
    granted_at = EXCLUDED.granted_at;

-- name: DeleteSubscriptionOperatorGrant :execrows
DELETE FROM purser.subscription_operator_grants
WHERE subscription_id = sqlc.arg(subscription_id)::text::uuid;

-- name: GetSubscriptionTierForGrant :one
-- The tier and billing model of the tenant's subscription, which an operator
-- grant applies to.
SELECT ts.id::text AS subscription_id, ts.tier_id::text AS tier_id, bt.tier_name,
       COALESCE(bt.tier_level, 0)::integer AS tier_level, ts.billing_model
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: GetProviderSubscriptionState :one
-- Whether a Stripe or Mollie subscription bills the tier's base fee on the
-- provider's side for this subscription.
SELECT ts.id::text AS subscription_id,
       (NULLIF(ts.stripe_subscription_id, '') IS NOT NULL)::boolean AS has_stripe_subscription,
       (NULLIF(ts.mollie_subscription_id, '') IS NOT NULL)::boolean AS has_mollie_subscription
FROM purser.tenant_subscriptions ts
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: GetActiveOperatorBaseFeeOverride :one
-- Whether an operator grant in force sets this tenant's base fee.
SELECT (g.base_price IS NOT NULL)::boolean AS overrides_base_fee
FROM purser.tenant_subscriptions ts
JOIN purser.subscription_operator_grants g ON g.subscription_id = ts.id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid AND ts.status != 'cancelled'
  AND (g.expires_at IS NULL OR g.expires_at > NOW())
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: CreateConfirmedOperatorInvoicePayment :exec
-- A payment the operator received outside any provider (a bank transfer) and
-- records against the invoice it pays.
INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at, updated_at,
    original_amount_cents, original_currency, eur_amount_cents,
    fx_units_per_eur, fx_source, fx_reference_date
) VALUES (
    sqlc.arg(payment_id)::text::uuid,
    sqlc.arg(invoice_id)::text::uuid,
    'bank_transfer',
    sqlc.arg(amount)::text::numeric,
    sqlc.arg(currency)::text,
    NULLIF(sqlc.arg(reference)::text, ''),
    'confirmed',
    sqlc.arg(confirmed_at),
    sqlc.arg(confirmed_at),
    sqlc.arg(confirmed_at),
    sqlc.arg(original_amount_cents)::bigint,
    sqlc.arg(currency)::text,
    sqlc.arg(eur_amount_cents)::bigint,
    sqlc.arg(fx_units_per_eur)::text::numeric,
    sqlc.arg(fx_source)::text,
    sqlc.arg(fx_reference_date)::date
);
