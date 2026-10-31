-- name: GetSubscriptionOperatorGrant :one
-- The operator grant recorded for a tenant's subscription, whether or not it
-- is still in force; active reports whether it applies now.
SELECT g.subscription_id::text AS subscription_id,
       g.base_price,
       g.waive_usage, g.collection, g.expires_at, g.reason, g.granted_by, g.granted_at,
       COALESCE(g.fallback_tier_id::text, '')::text AS fallback_tier_id, COALESCE(g.assigned_tier_id, ts.tier_id)::text AS assigned_tier_id,
       COALESCE(fallback.tier_name, '')::text AS fallback_tier_name,
       (g.expires_at IS NULL OR g.expires_at > NOW())::boolean AS active, g.expiry_error, g.expiry_failures
FROM purser.tenant_subscriptions ts
JOIN purser.subscription_operator_grants g ON g.subscription_id = ts.id
LEFT JOIN purser.billing_tiers fallback ON fallback.id = g.fallback_tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY ts.created_at DESC
LIMIT 1;

-- name: UpsertSubscriptionOperatorGrant :exec
INSERT INTO purser.subscription_operator_grants (
    subscription_id, base_price, waive_usage, collection, expires_at, reason, granted_by, granted_at, fallback_tier_id, assigned_tier_id
) VALUES (
    sqlc.arg(subscription_id)::text::uuid,
    sqlc.narg(base_price)::text::numeric,
    sqlc.arg(waive_usage),
    sqlc.arg(collection),
    sqlc.narg(expires_at),
    sqlc.arg(reason),
    NULLIF(sqlc.arg(granted_by)::text, ''),
    COALESCE(sqlc.narg(effective_at)::timestamptz, NOW()), sqlc.narg(fallback_tier_id)::text::uuid, sqlc.narg(assigned_tier_id)::text::uuid
)
ON CONFLICT (subscription_id) DO UPDATE SET
    base_price = EXCLUDED.base_price,
    waive_usage = EXCLUDED.waive_usage,
    collection = EXCLUDED.collection,
    expires_at = EXCLUDED.expires_at,
    reason = EXCLUDED.reason,
    granted_by = EXCLUDED.granted_by,
    granted_at = EXCLUDED.granted_at,
    fallback_tier_id = EXCLUDED.fallback_tier_id, assigned_tier_id = EXCLUDED.assigned_tier_id,
    expiry_attempted_at=NULL, expiry_error=NULL, expiry_failures=0;

-- name: DeleteSubscriptionOperatorGrant :execrows
DELETE FROM purser.subscription_operator_grants
WHERE subscription_id = sqlc.arg(subscription_id)::text::uuid;

-- name: GetSubscriptionTierForGrant :one
-- The tier and billing model of the tenant's subscription, which an operator
-- grant applies to.
SELECT ts.id::text AS subscription_id, ts.tier_id::text AS tier_id, bt.tier_name,
       COALESCE(bt.tier_level, 0)::integer AS tier_level, ts.billing_model, ts.status
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
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

-- name: CreateConfirmedOperatorInvoicePayment :exec
-- A payment the operator received outside any provider (a bank transfer) and
-- records against the invoice it pays. Its receipt is issued now, to the
-- invoice's tenant as its billing details are now.
INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at, updated_at,
    original_amount_cents, original_currency, eur_amount_cents,
    fx_units_per_eur, fx_source, fx_reference_date, customer_snapshot, supplier_snapshot
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
    sqlc.arg(fx_reference_date)::date,
    purser.billing_customer_snapshot((
        SELECT invoice.tenant_id FROM purser.billing_invoices invoice
        WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
    )),
    NULLIF(sqlc.arg(supplier_snapshot)::text, '')::jsonb
);

-- name: ListExpiredOperatorGrants :many
-- Platform-wide expiry queue; mutation rechecks the grant under its subscription lock.
SELECT ts.tenant_id::text AS tenant_id, g.granted_at FROM purser.subscription_operator_grants g
JOIN purser.tenant_subscriptions ts ON ts.id = g.subscription_id
WHERE g.expires_at <= NOW()
  AND (g.expiry_attempted_at IS NULL OR g.expiry_attempted_at + INTERVAL '5 minutes' <= NOW())
ORDER BY g.expiry_attempted_at ASC NULLS FIRST, g.expires_at ASC LIMIT 100;

-- name: ListOperatorBillingGrants :many
-- Bound each source before joining subscription/tier metadata. This operator
-- query deliberately spans tenants, including archived grants when requested.
WITH grants AS (
 (SELECT NULL::uuid AS tenant_id, g.subscription_id, NULL::uuid AS tier_id,
        g.fallback_tier_id, g.base_price, g.waive_usage, g.collection, g.expires_at,
        g.reason, g.granted_by, g.granted_at, g.expiry_error, g.expiry_failures, NULL::timestamptz AS ended_at
  FROM purser.subscription_operator_grants g
  WHERE (sqlc.arg(include_expired)::boolean OR g.expires_at IS NULL OR g.expires_at > NOW())
    AND (sqlc.arg(expiring_seconds)::bigint = 0 OR (g.expires_at > NOW() AND g.expires_at <= NOW() + sqlc.arg(expiring_seconds)::bigint * INTERVAL '1 second'))
  ORDER BY g.granted_at DESC, g.subscription_id
  LIMIT sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint)
 UNION ALL
 (SELECT tenant_id, subscription_id, tier_id, fallback_tier_id, base_price, waive_usage, collection,
        expires_at, reason, granted_by, granted_at, NULL::text AS expiry_error, 0::integer AS expiry_failures, ended_at
  FROM purser.subscription_operator_grant_history
  WHERE sqlc.arg(include_expired)::boolean
    AND (sqlc.arg(expiring_seconds)::bigint = 0 OR (expires_at > NOW() AND expires_at <= NOW() + sqlc.arg(expiring_seconds)::bigint * INTERVAL '1 second'))
  ORDER BY granted_at DESC, subscription_id, ended_at ASC
  LIMIT sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint)
), page AS (
 SELECT * FROM grants
 ORDER BY granted_at DESC, subscription_id, ended_at NULLS FIRST
 LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset)
)
SELECT COALESCE(g.tenant_id, ts.tenant_id)::text AS tenant_id, g.subscription_id::text AS subscription_id, t.tier_name,
       COALESCE(f.tier_name, '')::text AS fallback_tier_name,
       g.base_price, g.waive_usage, g.collection, g.expires_at, g.reason, g.granted_by, g.granted_at, g.expiry_error, g.expiry_failures, g.ended_at,
       (g.ended_at IS NULL AND (g.expires_at IS NULL OR g.expires_at > NOW()))::boolean AS active
FROM page g JOIN purser.tenant_subscriptions ts ON ts.id = g.subscription_id
JOIN purser.billing_tiers t ON t.id = COALESCE(g.tier_id, ts.tier_id)
LEFT JOIN purser.billing_tiers f ON f.id = g.fallback_tier_id
ORDER BY g.granted_at DESC, g.subscription_id, g.ended_at NULLS FIRST;

-- name: SetOperatorGrantBoundary :exec
-- The history trigger uses the same instant as the closing document.
SELECT set_config('frameworks.operator_grant_boundary', sqlc.arg(boundary)::text, true);

-- name: RecordOperatorGrantExpiryFailure :exec
UPDATE purser.subscription_operator_grants g
SET expiry_attempted_at=NOW(), expiry_error=sqlc.arg(failure)::text, expiry_failures=expiry_failures+1
FROM purser.tenant_subscriptions ts
WHERE ts.id=g.subscription_id AND ts.tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND g.expires_at <= NOW() AND g.granted_at=sqlc.arg(granted_at)::timestamptz
  AND (g.expiry_attempted_at IS NULL OR g.expiry_attempted_at <= NOW()-INTERVAL '5 minutes');

-- name: CountFailedOperatorGrantExpiries :one
-- Platform-wide operator recovery metric.
SELECT COUNT(*) FROM purser.subscription_operator_grants g
WHERE g.expiry_error IS NOT NULL;

-- name: CountRepeatedOperatorGrantExpiryFailures :one
-- Platform-wide recovery metric; one failed attempt alone is not a stuck grant.
SELECT COUNT(*) FROM purser.subscription_operator_grants WHERE expiry_error IS NOT NULL AND expiry_failures>1;
