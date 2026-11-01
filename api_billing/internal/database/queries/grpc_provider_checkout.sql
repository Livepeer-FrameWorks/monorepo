-- name: GetStripeTierCheckoutConfig :one
SELECT tier_name, COALESCE(currency, 'EUR')::text AS currency,
    COALESCE((CASE WHEN sqlc.arg(yearly)::boolean THEN stripe_price_id_yearly ELSE stripe_price_id_monthly END)::text,
    '')::text AS price_id
FROM purser.billing_tiers
WHERE id = sqlc.arg(tier_id)::text::uuid;

-- name: GetTenantPendingTierState :one
SELECT pending_reason, COALESCE(pending_tier_id::text, '')::text AS pending_tier_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: UpsertStripeTenantCheckoutIntent :one
-- Reuse only an outstanding attempt. Settled receipts and their provider keys
-- remain intact; a subsequent checkout gets its own durable idempotency key.
-- A retry retains its stored provider key and original request deadline.
WITH requested AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
    sqlc.arg(tier_id)::text::uuid AS tier_id,
    sqlc.arg(currency)::text AS currency,
    sqlc.arg(idempotency_key)::text AS idempotency_key
), active AS (
    SELECT i.id
    FROM purser.payment_provider_intents i, requested r
    WHERE i.tenant_id = r.tenant_id
    AND i.provider = 'stripe' AND i.purpose = 'tenant_subscription_checkout'
    AND (i.idempotency_key = r.idempotency_key OR left(i.idempotency_key,
    33) = md5(r.idempotency_key) || ':')
    AND (i.status IN ('pending', 'provider_open', 'provider_call_failed') OR (i.status = 'succeeded' AND i.provider_session_id IS NULL))
    ORDER BY i.created_at DESC LIMIT 1
), reused AS (
    UPDATE purser.payment_provider_intents
    SET attempt_count = attempt_count + 1, updated_at = NOW()
    WHERE id = (SELECT id FROM active)
    RETURNING id::text AS id
), created AS (
    INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, idempotency_key, expires_at, submission_state
    )
    SELECT r.tenant_id, 'stripe', 'tenant_subscription_checkout', 'billing_tiers', r.tier_id, 'pending',
    r.currency,
    md5(r.idempotency_key) || ':' || gen_random_uuid()::text, NOW() + INTERVAL '23 hours', 'prepared'
    FROM requested r WHERE NOT EXISTS (SELECT 1 FROM active)
    RETURNING id::text AS id
)
SELECT id FROM reused UNION ALL SELECT id FROM created;

-- name: SetProviderIntentCustomer :exec
UPDATE purser.payment_provider_intents
SET provider_customer_id = sqlc.arg(customer_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid
    AND tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND (provider_customer_id IS NULL OR provider_customer_id = sqlc.arg(customer_id));

-- name: SetProviderIntentSessionOpen :exec
WITH bound AS (
    UPDATE purser.payment_provider_intents
    SET provider_session_id = sqlc.arg(session_id),
        status = CASE WHEN status IN ('succeeded', 'cancelled', 'expired', 'terminal_failed')
                      THEN status ELSE 'provider_open' END,
        updated_at = NOW()
    WHERE id = sqlc.arg(intent_id)::text::uuid
      AND tenant_id = sqlc.arg(tenant_id)::text::uuid
    RETURNING id, tenant_id, status, recovery_started_at
)
UPDATE purser.provider_webhook_inbox
SET attempts = CASE WHEN status = 'processed' THEN 0 ELSE attempts END,
    wait_attempts = CASE WHEN status = 'processed' THEN 0 ELSE wait_attempts END,
    unresolved_since = CASE WHEN status = 'processed' THEN NULL ELSE unresolved_since END,
    last_error = CASE WHEN status = 'processed' THEN NULL ELSE last_error END,
    processed_at = NULL,
    status = CASE WHEN status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE,
    next_attempt_at = LEAST(next_attempt_at, NOW()), updated_at = NOW()
WHERE provider = 'checkout_recovery'
  AND event_key IN (
      SELECT 'checkout-expiry:' || id::text FROM bound
      WHERE status IN ('expired', 'cancelled', 'terminal_failed')
         OR recovery_started_at IS NOT NULL
  );

-- name: LinkProviderIntentCustomer :exec
UPDATE purser.payment_provider_intents
SET provider_customer_id = sqlc.arg(customer_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid
    AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: StageStripeCheckoutTier :execrows
UPDATE purser.tenant_subscriptions
SET pending_tier_id = sqlc.arg(tier_id)::text::uuid,
    pending_reason = 'stripe_checkout', pending_effective_at = NULL,
    pending_intent_id = sqlc.arg(intent_id)::text::uuid, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND (pending_tier_id IS NULL OR pending_reason = 'stripe_checkout')
    AND EXISTS(SELECT 1 FROM purser.payment_provider_intents i WHERE i.id = sqlc.arg(intent_id)::text::uuid AND i.tenant_id = sqlc.arg(tenant_id)::text::uuid AND i.status NOT IN ('succeeded', 'cancelled', 'expired', 'terminal_failed'));

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
SET stripe_subscription_status = sqlc.narg(status)::text,
    stripe_current_period_end = CASE WHEN sqlc.narg(period_end)::timestamptz IS NULL THEN stripe_current_period_end WHEN sqlc.narg(period_end)::timestamptz IS NOT NULL AND (billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start) THEN sqlc.narg(period_end)::timestamptz WHEN billing_period_start IS NULL OR sqlc.narg(period_end)::timestamp > billing_period_start THEN GREATEST(sqlc.narg(period_end)::timestamptz,
    stripe_current_period_end) ELSE stripe_current_period_end END,
    billing_period_start = CASE WHEN ((free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC') OR sqlc.narg(status)::text IN ('past_due', 'unpaid', 'canceled')) THEN billing_period_start ELSE GREATEST(sqlc.narg(period_start)::timestamp, billing_period_start) END,
    billing_period_end = CASE WHEN ((free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC') OR sqlc.narg(status)::text IN ('past_due', 'unpaid', 'canceled')) THEN billing_period_end ELSE CASE WHEN sqlc.narg(period_end)::timestamptz IS NOT NULL AND (billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start) THEN sqlc.narg(period_end)::timestamp WHEN billing_period_start IS NULL OR sqlc.narg(period_end)::timestamp > billing_period_start THEN GREATEST(sqlc.narg(period_end)::timestamp,
    billing_period_end) ELSE billing_period_end END END,
    next_billing_date = CASE WHEN ((free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC') OR sqlc.narg(status)::text IN ('past_due', 'unpaid', 'canceled')) THEN next_billing_date ELSE CASE WHEN sqlc.narg(period_end)::timestamptz IS NOT NULL AND (billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start) THEN sqlc.narg(period_end)::timestamp WHEN billing_period_start IS NULL OR sqlc.narg(period_end)::timestamp > billing_period_start THEN GREATEST(sqlc.narg(period_end)::timestamp,
    next_billing_date) ELSE next_billing_date END END,
    free_fallback_unbilled_through = CASE WHEN sqlc.narg(status)::text IN ('past_due', 'unpaid', 'canceled') THEN GREATEST(free_fallback_unbilled_through, NOW(), sqlc.narg(period_start)::timestamptz - INTERVAL '1 microsecond') WHEN (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC') THEN GREATEST(free_fallback_unbilled_through, sqlc.narg(period_start)::timestamptz - INTERVAL '1 microsecond') ELSE free_fallback_unbilled_through END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND stripe_subscription_id = sqlc.arg(subscription_id)::text
    AND NOT free_fallback_provider_stopping AND free_fallback_provider_ended_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = tenant_subscriptions.tenant_id);

-- name: GetMollieTierPrice :one
SELECT tier_name, COALESCE(base_price::text, '0')::text AS base_price,
    COALESCE(currency, 'EUR')::text AS currency
FROM purser.billing_tiers
WHERE id = sqlc.arg(tier_id)::text::uuid;

-- name: HasUnusedPaidMollieFunding :one
-- Called under the subscription lock before reserving another first payment.
-- A recorded mandate binds retained activation funding to this contract.
-- Unbound delivery receipts are reconciled through historical recovery.
SELECT EXISTS (
    SELECT 1 FROM purser.payment_provider_intents funding
    WHERE funding.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND funding.provider = 'mollie' AND funding.purpose = 'mollie_first_payment'
      AND funding.status = 'succeeded' AND funding.amount_cents > 0
      AND funding.provider_payment_id IS NOT NULL AND funding.provider_mandate_id IS NOT NULL
      AND funding.activated_at IS NULL
)::boolean AS unused;

-- name: UpsertMollieFirstPaymentIntent :one
-- Reuse only an outstanding attempt. Settled receipts and their provider keys
-- remain intact; a subsequent checkout gets its own durable idempotency key.
-- A retry retains its stored provider key and original request deadline.
WITH requested AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
    sqlc.arg(tier_id)::text::uuid AS tier_id,
    sqlc.arg(currency)::text AS currency,
    sqlc.arg(amount_cents)::bigint AS amount_cents,
    sqlc.arg(idempotency_key)::text AS idempotency_key
), active AS (
    SELECT i.id
    FROM purser.payment_provider_intents i, requested r
    WHERE i.tenant_id = r.tenant_id
    AND i.provider = 'mollie' AND i.purpose = 'mollie_first_payment'
    AND (i.idempotency_key = r.idempotency_key OR left(i.idempotency_key,
    33) = md5(r.idempotency_key) || ':')
    AND (i.status IN ('pending', 'provider_open', 'provider_call_failed') OR (i.status = 'succeeded' AND i.provider_payment_id IS NULL))
    ORDER BY i.created_at DESC LIMIT 1
), reused AS (
    UPDATE purser.payment_provider_intents
    SET attempt_count = attempt_count + 1, updated_at = NOW()
    WHERE id = (SELECT id FROM active)
    RETURNING id::text AS id
), created AS (
    INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, idempotency_key, amount_cents, expires_at, submission_state
    )
    SELECT r.tenant_id, 'mollie', 'mollie_first_payment', 'billing_tiers', r.tier_id, 'pending',
    r.currency,
    md5(r.idempotency_key) || ':' || gen_random_uuid()::text, r.amount_cents, NOW() + INTERVAL '23 hours', 'prepared'
    FROM requested r WHERE NOT EXISTS (SELECT 1 FROM active)
    RETURNING id::text AS id
)
SELECT id FROM reused UNION ALL SELECT id FROM created;

-- name: GetMollieCustomerID :one
SELECT mollie_customer_id
FROM purser.mollie_customers
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: LockProviderIntentPaymentID :one
-- Locks the intent row and returns the provider payment it already names.
SELECT COALESCE(provider_payment_id, '')::text AS provider_payment_id
FROM purser.payment_provider_intents
WHERE id = sqlc.arg(intent_id)::text::uuid
    AND tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE;

-- name: SetProviderIntentPaymentOpen :exec
WITH bound AS (
    UPDATE purser.payment_provider_intents
    SET provider_payment_id = sqlc.arg(payment_id),
        status = CASE WHEN status IN ('succeeded', 'cancelled', 'expired', 'terminal_failed')
                      THEN status ELSE 'provider_open' END,
        updated_at = NOW()
    WHERE id = sqlc.arg(intent_id)::text::uuid
      AND tenant_id = sqlc.arg(tenant_id)::text::uuid
    RETURNING id, tenant_id, status, recovery_started_at
)
UPDATE purser.provider_webhook_inbox
SET attempts = CASE WHEN status = 'processed' THEN 0 ELSE attempts END,
    wait_attempts = CASE WHEN status = 'processed' THEN 0 ELSE wait_attempts END,
    unresolved_since = CASE WHEN status = 'processed' THEN NULL ELSE unresolved_since END,
    last_error = CASE WHEN status = 'processed' THEN NULL ELSE last_error END,
    processed_at = NULL,
    status = CASE WHEN status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE,
    next_attempt_at = LEAST(next_attempt_at, NOW()), updated_at = NOW()
WHERE provider = 'checkout_recovery'
  AND event_key IN (
      SELECT 'checkout-expiry:' || id::text FROM bound
      WHERE status IN ('expired', 'cancelled', 'terminal_failed')
         OR recovery_started_at IS NOT NULL
  );

-- name: TenantSubscriptionExists :one
SELECT EXISTS(
    SELECT 1 FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
);

-- name: UpsertMollieSubscriptionIntent :one
-- Reuse only an outstanding attempt. Settled receipts and their provider keys
-- remain intact; a subsequent checkout gets its own durable idempotency key.
-- A retry retains its stored provider key and original request deadline.
-- A receipt bound to established service is not a new checkout to resume.
WITH requested AS (
    SELECT sqlc.arg(tenant_id)::text::uuid AS tenant_id,
    sqlc.arg(tier_id)::text::uuid AS tier_id,
    sqlc.narg(customer_id)::text AS customer_id,
    sqlc.arg(currency)::text AS currency,
    sqlc.arg(amount_cents)::bigint AS amount_cents,
    sqlc.arg(idempotency_key)::text AS idempotency_key
), active AS (
    SELECT i.id
    FROM purser.payment_provider_intents i, requested r
    WHERE i.tenant_id = r.tenant_id
    AND i.provider = 'mollie' AND i.purpose = 'mollie_subscription_create'
    AND (i.idempotency_key = r.idempotency_key OR left(i.idempotency_key,
    33) = md5(r.idempotency_key) || ':')
    AND (i.status IN ('pending', 'provider_open', 'provider_call_failed') OR (i.status = 'succeeded' AND i.provider_subscription_id IS NULL))
    AND NOT EXISTS (
        SELECT 1 FROM purser.tenant_subscriptions subscription
        WHERE subscription.tenant_id = i.tenant_id
          AND subscription.mollie_subscription_id = i.provider_subscription_id
    )
    ORDER BY i.created_at DESC LIMIT 1
), reused AS (
    UPDATE purser.payment_provider_intents
    SET attempt_count = attempt_count + 1, updated_at = NOW()
    WHERE id = (SELECT id FROM active)
    RETURNING id::text AS id
), created AS (
    INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, idempotency_key, provider_customer_id, amount_cents, expires_at, submission_state
    )
    SELECT r.tenant_id, 'mollie', 'mollie_subscription_create', 'billing_tiers', r.tier_id,
    'pending', r.currency,
    md5(r.idempotency_key) || ':' || gen_random_uuid()::text, r.customer_id, r.amount_cents, NOW() + INTERVAL '23 hours', 'prepared'
    FROM requested r WHERE NOT EXISTS (SELECT 1 FROM active)
    RETURNING id::text AS id
)
SELECT id FROM reused UNION ALL SELECT id FROM created;

-- name: SetProviderIntentSubscriptionOpen :exec
WITH bound AS (
    UPDATE purser.payment_provider_intents
    SET provider_subscription_id = sqlc.arg(subscription_id),
        status = CASE WHEN status IN ('succeeded', 'cancelled', 'expired', 'terminal_failed')
                      THEN status ELSE 'provider_open' END,
        updated_at = NOW()
    WHERE id = sqlc.arg(intent_id)::text::uuid
      AND tenant_id = sqlc.arg(tenant_id)::text::uuid
    RETURNING id, tenant_id, status, recovery_started_at
)
UPDATE purser.provider_webhook_inbox
SET attempts = CASE WHEN status = 'processed' THEN 0 ELSE attempts END,
    wait_attempts = CASE WHEN status = 'processed' THEN 0 ELSE wait_attempts END,
    unresolved_since = CASE WHEN status = 'processed' THEN NULL ELSE unresolved_since END,
    last_error = CASE WHEN status = 'processed' THEN NULL ELSE last_error END,
    processed_at = NULL,
    status = CASE WHEN status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE,
    next_attempt_at = LEAST(next_attempt_at, NOW()), updated_at = NOW()
WHERE provider = 'checkout_recovery'
  AND event_key IN (
      SELECT 'checkout-expiry:' || id::text FROM bound
      WHERE status IN ('expired', 'cancelled', 'terminal_failed')
         OR recovery_started_at IS NOT NULL
  );

-- name: ActivateMollieTenantSubscription :execrows
UPDATE purser.tenant_subscriptions
SET mollie_subscription_id = sqlc.narg(subscription_id)::text,
    mollie_next_payment_date = CASE WHEN (mollie_subscription_id = sqlc.narg(subscription_id)::text AND (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC')) THEN mollie_next_payment_date ELSE CASE WHEN billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start THEN sqlc.arg(next_payment_date)::date ELSE GREATEST(sqlc.arg(next_payment_date)::date,
    mollie_next_payment_date) END END,
    next_billing_date = CASE WHEN (mollie_subscription_id = sqlc.narg(subscription_id)::text AND (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC')) THEN next_billing_date ELSE CASE WHEN billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start THEN sqlc.arg(next_payment_date)::timestamp ELSE GREATEST(sqlc.arg(next_payment_date)::timestamp,
    next_billing_date) END END,
    billing_period_start = CASE WHEN (mollie_subscription_id = sqlc.narg(subscription_id)::text AND (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC')) THEN billing_period_start ELSE GREATEST(sqlc.arg(period_start), billing_period_start) END,
    billing_period_end = CASE WHEN (mollie_subscription_id = sqlc.narg(subscription_id)::text AND (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC')) THEN billing_period_end ELSE CASE WHEN billing_period_start IS NULL OR sqlc.narg(period_start)::timestamp >= billing_period_start THEN sqlc.narg(period_end)::timestamp ELSE GREATEST(sqlc.narg(period_end)::timestamp,
    billing_period_end) END END,
    tier_id = sqlc.arg(tier_id)::text::uuid,
    payment_method = 'mollie', status = 'active', billing_model = 'postpaid',
    free_fallback_provider_stopping = FALSE, free_fallback_wait_since = NULL, free_fallback_provider_ended_at = NULL,
    free_fallback_at = NULL, free_fallback_reason = NULL, free_fallback_notified_at = NULL, free_fallback_moved_at = NULL,
    pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL,
    free_fallback_unbilled_through = CASE WHEN mollie_subscription_id = sqlc.narg(subscription_id)::text THEN CASE WHEN (free_fallback_at IS NOT NULL OR billing_period_start <= free_fallback_unbilled_through AT TIME ZONE 'UTC') THEN GREATEST(free_fallback_unbilled_through, sqlc.arg(period_start)::timestamptz - INTERVAL '1 microsecond') ELSE free_fallback_unbilled_through END ELSE NULL END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetTenantMollieSubscriptionID :one
SELECT mollie_subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CancelLocalMollieSubscription :exec
-- The paid period runs out, then the sweep moves the tenant to Free. Mollie
-- reports no end event, so the date is set here.
UPDATE purser.tenant_subscriptions
SET status = 'pending_cancellation',
    free_fallback_provider_ended_at = COALESCE(free_fallback_provider_ended_at, NOW()),
    cancelled_at = NOW(),
    free_fallback_at = LEAST(COALESCE(free_fallback_at, 'infinity'::timestamptz), GREATEST(billing_period_end AT TIME ZONE 'UTC', NOW())),
    free_fallback_reason = 'cancelled',
    free_fallback_notified_at = COALESCE(free_fallback_notified_at, NOW()),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND mollie_subscription_id = sqlc.arg(subscription_id)::text
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id=tenant_subscriptions.tenant_id);

-- name: LockTenantSubscriptionForProviderChange :one
-- The subscription lock precedes separate grant and account-closure reads.
SELECT id::text AS subscription_id, tier_id::text AS tier_id, status, billing_model,
    stripe_subscription_id, mollie_subscription_id, pending_reason, free_fallback_provider_stopping, free_fallback_provider_ended_at
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE;

-- name: CompleteMollieSubscriptionCheckout :exec
WITH activated AS (
    UPDATE purser.payment_provider_intents
    SET status = 'succeeded', succeeded_at = COALESCE(succeeded_at, NOW()),
        activated_at = COALESCE(activated_at, NOW()), updated_at = NOW()
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND provider = 'mollie'
      AND purpose = 'mollie_subscription_create'
      AND provider_subscription_id = sqlc.arg(subscription_id)::text
    RETURNING funding_intent_id
)
UPDATE purser.payment_provider_intents
SET activated_at = COALESCE(activated_at, NOW()), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND id IN (SELECT funding_intent_id FROM activated);

-- name: LinkMollieCheckoutFunding :exec
UPDATE purser.payment_provider_intents native
SET funding_intent_id = (
    SELECT funding.id FROM purser.payment_provider_intents funding
    WHERE funding.tenant_id = native.tenant_id AND funding.provider = 'mollie'
      AND funding.purpose = 'mollie_first_payment'
      AND funding.provider_mandate_id = sqlc.arg(mandate_id)::text
      AND funding.provider_customer_id = native.provider_customer_id
      AND funding.status = 'succeeded' AND funding.activated_at IS NULL
      AND funding.recovery_started_at IS NULL AND funding.provider_payment_id IS NOT NULL
      AND funding.local_reference_type = 'billing_tiers'
      AND native.local_reference_type = 'billing_tiers'
      AND funding.local_reference_id = native.local_reference_id
      AND funding.currency = native.currency
      AND (native.amount_cents = 0 OR funding.amount_cents > 0)
    ORDER BY funding.created_at DESC, funding.id DESC LIMIT 1
), updated_at = NOW()
WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND native.id = sqlc.arg(intent_id)::text::uuid
  AND native.funding_intent_id IS NULL;

-- name: MollieCheckoutFundingMatches :one
-- The paid first month belongs to the purchased tier, customer and currency.
-- Its fee may differ from a later catalog renewal price for that same tier.
SELECT EXISTS (
    SELECT 1 FROM purser.payment_provider_intents native
    JOIN purser.payment_provider_intents funding
      ON funding.tenant_id = native.tenant_id AND funding.id = native.funding_intent_id
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.id = sqlc.arg(intent_id)::text::uuid
      AND native.provider = 'mollie' AND native.purpose = 'mollie_subscription_create'
      AND funding.provider = 'mollie' AND funding.purpose = 'mollie_first_payment'
      AND funding.provider_customer_id = native.provider_customer_id
      AND funding.local_reference_type = 'billing_tiers'
      AND native.local_reference_type = 'billing_tiers'
      AND funding.local_reference_id = native.local_reference_id
      AND funding.currency = native.currency
      AND (native.amount_cents = 0 OR funding.amount_cents > 0)
)::boolean AS matches;

-- name: CheckoutAlreadySettled :one
SELECT EXISTS (
    SELECT 1 FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND provider = sqlc.arg(provider)::text AND status IN ('succeeded', 'expired', 'cancelled', 'terminal_failed')
    AND ((NULLIF(sqlc.arg(session_id)::text, '') IS NOT NULL AND provider_session_id = sqlc.arg(session_id)::text)
    OR (NULLIF(sqlc.arg(payment_id)::text, '') IS NOT NULL AND provider_payment_id = sqlc.arg(payment_id)::text)))::boolean AS settled;

-- name: GetProviderCheckoutReservationKey :one
SELECT idempotency_key FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid;

-- name: GetProviderCheckoutReservationStatus :one
SELECT status FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid;

-- name: CheckoutReceiptBound :one
SELECT EXISTS(SELECT 1 FROM purser.payment_provider_intents
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND provider = sqlc.arg(provider)::text
    AND ((NULLIF(sqlc.arg(session_id)::text, '') IS NOT NULL AND provider_session_id = sqlc.arg(session_id)::text)
    OR (NULLIF(sqlc.arg(payment_id)::text, '') IS NOT NULL AND provider_payment_id = sqlc.arg(payment_id)::text)))::boolean AS bound;

-- name: FinishProviderCheckoutIntent :exec
-- Keep the subscription-before-intent lock order used by checkout reservations.
WITH locked AS (
    SELECT id FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid FOR UPDATE
), finished AS (
UPDATE purser.payment_provider_intents
SET status = CASE WHEN status IN ('expired', 'cancelled', 'terminal_failed') THEN status ELSE sqlc.arg(status)::text END, recovery_outcome = COALESCE(NULLIF(sqlc.arg(outcome)::text, ''), recovery_outcome),
    provider_session_id = COALESCE(sqlc.narg(session_id)::text, provider_session_id),
    provider_subscription_id = COALESCE(sqlc.narg(subscription_id)::text, provider_subscription_id), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid AND status<>'succeeded'
    AND EXISTS(SELECT 1 FROM locked)
RETURNING id
)
UPDATE purser.tenant_subscriptions SET pending_tier_id = NULL, pending_reason = NULL,
    pending_effective_at = NULL, pending_intent_id = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND pending_reason = 'stripe_checkout'
    AND pending_intent_id IN (SELECT id FROM finished)
    AND sqlc.arg(status)::text IN ('expired', 'cancelled', 'terminal_failed');

-- name: GetProviderCheckoutReservation :one
SELECT id::text, tenant_id::text, provider, purpose, provider_customer_id, provider_session_id,
    provider_payment_id, provider_subscription_id, status, COALESCE(expires_at,
    created_at+INTERVAL '24 hours') AS expires_at, attempt_count, checkout_completed_at,
    recovery_started_at, created_at, amount_cents, currency, expires_at AS provider_deadline, recovery_outcome,
    provider_request, submission_state, succeeded_at, activated_at, COALESCE(funding_intent_id::text, '')::text AS funding_intent_id, provider_mandate_id,
    COALESCE(reconciled_references = jsonb_build_object('session_id', provider_session_id, 'payment_id', provider_payment_id, 'subscription_id', provider_subscription_id), false)::boolean AS compensation_current
FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid;

-- name: EnqueueCheckoutReservationExpiry :exec
-- The provider inbox owns durable recovery, backoff and alerting after a deadline.
INSERT INTO purser.provider_webhook_inbox(provider, event_key, headers, raw_payload, next_attempt_at)
SELECT 'checkout_recovery', 'checkout-expiry:'||id::text, jsonb_build_object('tenant_id', tenant_id::text),
    convert_to(jsonb_build_object('checkout_reservation_id', id::text, 'tenant_id', tenant_id::text)::text, 'UTF8'), COALESCE(expires_at, created_at+INTERVAL '24 hours')
FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
ON CONFLICT(provider, event_key) DO NOTHING;

-- name: BindProviderCheckoutReceipt :exec
UPDATE purser.payment_provider_intents
SET provider_session_id = COALESCE(provider_session_id, NULLIF(sqlc.arg(session_id)::text, '')),
    provider_payment_id = COALESCE(provider_payment_id, NULLIF(sqlc.arg(payment_id)::text, '')),
    provider_subscription_id = COALESCE(provider_subscription_id, NULLIF(sqlc.arg(subscription_id)::text,
    '')), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid AND provider = sqlc.arg(provider)::text;

-- name: CompleteProviderCheckoutReservation :exec
-- Activation and receipt settlement commit in the same subscription transaction.
UPDATE purser.payment_provider_intents
SET status = 'succeeded', succeeded_at = COALESCE(succeeded_at, NOW()),
    activated_at = COALESCE(activated_at, NOW()), recovery_started_at = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid;

-- name: SetProviderCheckoutDeadline :exec
-- The provider payment controls when its open checkout stops accepting completion.
WITH bounded AS (
    UPDATE purser.payment_provider_intents SET expires_at = sqlc.arg(expires_at)::timestamptz
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
    RETURNING id, expires_at
)
UPDATE purser.provider_webhook_inbox SET next_attempt_at = (SELECT expires_at FROM bounded)
WHERE provider = 'checkout_recovery' AND event_key = 'checkout-expiry:'||sqlc.arg(intent_id)::text AND status = 'pending';

-- name: RequeueExpiredCheckoutReservation :exec
-- A late callback can reveal a provider object after an earlier empty recovery.
UPDATE purser.provider_webhook_inbox SET  attempts = CASE WHEN status = 'processed' THEN 0 ELSE attempts END,
    wait_attempts = CASE WHEN status = 'processed' THEN 0 ELSE wait_attempts END,
    unresolved_since = CASE WHEN status = 'processed' THEN NULL ELSE unresolved_since END,
    last_error = CASE WHEN status = 'processed' THEN NULL ELSE last_error END,
    processed_at = NULL,
status = CASE WHEN status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE, next_attempt_at = NOW(), updated_at = NOW()
WHERE provider = 'checkout_recovery' AND event_key = 'checkout-expiry:'||sqlc.arg(intent_id)::text AND status IN ('pending', 'failed', 'processing', 'processed')
    AND EXISTS(SELECT 1 FROM purser.payment_provider_intents
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid);

-- name: FindProviderCheckoutReservation :one
SELECT id::text FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND provider = sqlc.arg(provider)::text
    AND purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create')
    AND ((NULLIF(sqlc.arg(session_id)::text, '') IS NOT NULL AND provider_session_id = sqlc.arg(session_id)::text)
    OR (NULLIF(sqlc.arg(payment_id)::text, '') IS NOT NULL AND provider_payment_id = sqlc.arg(payment_id)::text)
    OR (NULLIF(sqlc.arg(subscription_id)::text,
    '') IS NOT NULL AND provider_subscription_id = sqlc.arg(subscription_id)::text))
LIMIT 1;

-- name: MarkCheckoutCompleted :exec
-- Completion is durable independently of payment settlement.
UPDATE purser.payment_provider_intents
SET checkout_completed_at = COALESCE(checkout_completed_at, sqlc.arg(completed_at)::timestamptz), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
    AND recovery_started_at IS NULL AND status NOT IN ('expired', 'cancelled', 'terminal_failed');

-- name: StartCheckoutCompensation :exec
UPDATE purser.payment_provider_intents
SET recovery_started_at = COALESCE(recovery_started_at, NOW()), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
    AND (status <> 'succeeded' OR (purpose = 'mollie_first_payment' AND activated_at IS NULL));

-- name: GetPendingProviderCheckout :one
SELECT COUNT(*)>0 AS pending,
    COALESCE(MIN(deadline), 'epoch'::timestamptz)::timestamptz AS deadline,
    COALESCE(MAX(kind), 'checkout')::text AS kind
FROM (
    SELECT COALESCE(intent.expires_at, intent.created_at + INTERVAL '24 hours') AS deadline, 'checkout'::text AS kind
    FROM purser.payment_provider_intents intent
    WHERE intent.tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND (sqlc.arg(exclude_id)::text = '' OR intent.id::text <>sqlc.arg(exclude_id)::text)
    AND intent.purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create')
    AND intent.status IN ('pending', 'provider_open', 'provider_call_failed')
    AND NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions ts
    WHERE ts.tenant_id = intent.tenant_id
    AND ((intent.provider = 'stripe' AND intent.provider_subscription_id = ts.stripe_subscription_id)
    OR (intent.provider = 'mollie' AND intent.provider_subscription_id = ts.mollie_subscription_id)))
    UNION ALL
    SELECT requested_at, 'tier_upgrade'::text
    FROM purser.subscription_tier_upgrades
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND state IN ('requested', 'provider_applied', 'pricing_applied')
    UNION ALL
    SELECT free_fallback_at, 'free_fallback'::text
    FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND free_fallback_provider_stopping
) pending;

-- name: EnqueueUnresolvedCheckoutRecovery :exec
-- Service-wide recovery catches receipts whose provider response preceded this process.
INSERT INTO purser.provider_webhook_inbox(provider, event_key, headers, raw_payload, next_attempt_at)
SELECT 'checkout_recovery', 'checkout-expiry:'||id::text, jsonb_build_object('tenant_id', tenant_id::text),
    convert_to(jsonb_build_object('checkout_reservation_id', id::text, 'tenant_id', tenant_id::text)::text, 'UTF8'), COALESCE(expires_at, created_at+INTERVAL '24 hours')
FROM purser.payment_provider_intents
WHERE purpose IN ('tenant_subscription_checkout', 'mollie_first_payment', 'mollie_subscription_create')
    AND status IN ('pending', 'provider_open', 'provider_call_failed')
    AND NOT EXISTS(SELECT 1 FROM purser.provider_webhook_inbox i WHERE i.provider = 'checkout_recovery' AND i.event_key = 'checkout-expiry:'||purser.payment_provider_intents.id::text)
ORDER BY created_at LIMIT 1000
ON CONFLICT(provider, event_key) DO NOTHING;

-- name: SaveCheckoutProviderRequest :one
-- Persist the original request before provider I/O; retries retain identical parameters.
-- An unjournalled request with unknown submission provenance may only be located.
UPDATE purser.payment_provider_intents
SET provider_request = CASE WHEN submission_state = 'unknown' AND provider_request IS NULL THEN NULL ELSE COALESCE(provider_request, sqlc.arg(request)::bytea) END,
    submission_state = CASE WHEN submission_state = 'prepared' THEN 'submitted' ELSE submission_state END, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
    AND status IN ('pending', 'provider_open', 'provider_call_failed') AND recovery_started_at IS NULL
RETURNING provider_request, submission_state;

-- name: FinishCheckoutCompensation :one
-- Binding can arrive during provider I/O; only release the exact references reconciled.
WITH locked AS (
    SELECT id FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid FOR UPDATE
), finished AS (
    UPDATE purser.payment_provider_intents

SET status = CASE WHEN status IN ('expired', 'cancelled', 'terminal_failed') THEN status ELSE 'expired' END,
    recovery_outcome = sqlc.arg(outcome)::text,
    reconciled_references = sqlc.arg(expected_references)::jsonb, updated_at = NOW()
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
    AND (status <> 'succeeded' OR (purpose = 'mollie_first_payment' AND activated_at IS NULL))
    AND EXISTS (SELECT 1 FROM locked)
    AND jsonb_build_object('session_id', provider_session_id, 'payment_id', provider_payment_id,
    'subscription_id', provider_subscription_id) = sqlc.arg(expected_references)::jsonb
    RETURNING id
), cleared AS (
    UPDATE purser.tenant_subscriptions
    SET pending_tier_id = NULL, pending_reason = NULL, pending_effective_at = NULL,
    pending_intent_id = NULL, updated_at = NOW()
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND pending_reason = 'stripe_checkout'
    AND pending_intent_id IN (SELECT id FROM finished)
    RETURNING id
)
SELECT COUNT(*)::bigint FROM finished;

-- name: HasPendingMollieFundingCheckout :one
SELECT EXISTS (
    SELECT 1 FROM purser.payment_provider_intents native
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.funding_intent_id = sqlc.arg(funding_id)::text::uuid
      AND native.activated_at IS NULL
      AND (native.status IN ('pending', 'provider_open', 'provider_call_failed')
        OR (native.recovery_started_at IS NOT NULL AND NOT COALESCE(
            native.reconciled_references = jsonb_build_object('session_id',native.provider_session_id,
                'payment_id',native.provider_payment_id,'subscription_id',native.provider_subscription_id),false)))
)::boolean AS pending;

-- name: MarkMollieFirstPaymentActivated :exec
UPDATE purser.payment_provider_intents
SET activated_at = COALESCE(activated_at, NOW()), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
  AND provider = 'mollie' AND purpose = 'mollie_first_payment';

-- name: ListRefusedMollieFundingIntents :many
-- The subscription lock excludes activation while unused, locally owned funding
-- and its unactivated native checkout are handed to durable compensation.
WITH unused AS (
    SELECT funding.id
    FROM purser.payment_provider_intents funding
    JOIN purser.mollie_customers customer ON customer.tenant_id = funding.tenant_id
      AND customer.mollie_customer_id = funding.provider_customer_id
    WHERE funding.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND funding.provider = 'mollie' AND funding.purpose = 'mollie_first_payment'
      AND funding.local_reference_type = 'billing_tiers'
      AND (sqlc.arg(mandate_id)::text <> ''
        OR funding.local_reference_id = sqlc.arg(tier_id)::text::uuid)
      AND funding.status = 'succeeded' AND funding.activated_at IS NULL
      AND funding.provider_mandate_id IS NOT NULL
      AND (sqlc.arg(mandate_id)::text = '' OR funding.provider_mandate_id = sqlc.arg(mandate_id)::text)
      AND funding.provider_payment_id IS NOT NULL
      AND funding.recovery_started_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM purser.payment_provider_intents native
          JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = native.tenant_id
          WHERE native.tenant_id = funding.tenant_id AND native.funding_intent_id = funding.id
            AND (native.activated_at IS NOT NULL
              OR native.provider_subscription_id = subscription.mollie_subscription_id))
)
SELECT id::text FROM unused
UNION
SELECT native.id::text FROM purser.payment_provider_intents native
JOIN unused ON native.funding_intent_id = unused.id
WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND native.provider = 'mollie' AND native.purpose = 'mollie_subscription_create'
  AND native.activated_at IS NULL AND native.status <> 'succeeded';

-- name: GetBoundMollieSubscriptionCustomer :one
-- The native receipt binds its collector to the original provider customer.
-- Missing or conflicting bindings cannot prove a cancelled collector ended.
SELECT MIN(provider_customer_id)::text AS customer_id
FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND provider = 'mollie' AND purpose = 'mollie_subscription_create'
  AND provider_subscription_id = sqlc.arg(subscription_id)::text
  AND provider_customer_id IS NOT NULL
GROUP BY tenant_id
HAVING COUNT(DISTINCT provider_customer_id) = 1;

-- name: ListUnresolvedMollieNativeCheckouts :many
-- A provider-bound native receipt needs its terminal state checked before a
-- returning customer can replace it. A live or unknown subscription stays fenced.
SELECT intent.id::text
FROM purser.payment_provider_intents intent
WHERE intent.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND intent.provider = 'mollie' AND intent.purpose = 'mollie_subscription_create'
  AND intent.status IN ('pending', 'provider_open', 'provider_call_failed')
  AND intent.activated_at IS NULL AND intent.recovery_started_at IS NULL
  AND intent.provider_subscription_id IS NOT NULL AND intent.provider_customer_id IS NOT NULL
  AND COALESCE(intent.expires_at, intent.created_at + INTERVAL '24 hours') <= NOW()
  AND NOT EXISTS (
      SELECT 1 FROM purser.tenant_subscriptions subscription
      WHERE subscription.tenant_id = intent.tenant_id
        AND subscription.mollie_subscription_id = intent.provider_subscription_id
  )
ORDER BY intent.created_at, intent.id;
