-- name: StartSubscriptionFreeFallback :execrows
-- A repeated provider event never extends the date. A voluntary cancellation
-- has no grace notice to send, so it is marked as already notified.
UPDATE purser.tenant_subscriptions
SET free_fallback_unbilled_through = CASE WHEN free_fallback_at IS NULL THEN GREATEST(free_fallback_unbilled_through, NOW()) ELSE free_fallback_unbilled_through END,
    free_fallback_attempted_at = CASE WHEN free_fallback_at IS NULL THEN NULL ELSE free_fallback_attempted_at END,
    free_fallback_failures = CASE WHEN free_fallback_at IS NULL THEN 0 ELSE free_fallback_failures END,
    free_fallback_error = CASE WHEN free_fallback_at IS NULL THEN NULL ELSE free_fallback_error END,
    free_fallback_notice_attempted_at = CASE WHEN free_fallback_at IS NULL THEN NULL ELSE free_fallback_notice_attempted_at END,
    free_fallback_notice_failures = CASE WHEN free_fallback_at IS NULL THEN 0 ELSE free_fallback_notice_failures END,
    free_fallback_notice_error = CASE WHEN free_fallback_at IS NULL THEN NULL ELSE free_fallback_notice_error END,
    free_fallback_notice_abandoned_at = CASE WHEN free_fallback_at IS NULL THEN NULL ELSE free_fallback_notice_abandoned_at END,
    free_fallback_reason = CASE
        WHEN free_fallback_at IS NULL OR sqlc.arg(fallback_at)::timestamptz < free_fallback_at THEN sqlc.arg(reason)::text
        ELSE free_fallback_reason END,
    free_fallback_at = LEAST(COALESCE(free_fallback_at, sqlc.arg(fallback_at)::timestamptz), sqlc.arg(fallback_at)::timestamptz),
    free_fallback_notified_at = COALESCE(free_fallback_notified_at, CASE WHEN sqlc.arg(reason)::text = 'cancelled' THEN NOW() END),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_moved_at IS NULL;

-- name: MarkSubscriptionPaymentFailed :execrows
-- A collector that is gone leaves a paid postpaid subscription past due for its
-- grace period. Free has nothing to collect, so it never enters one.
UPDATE purser.tenant_subscriptions subscription
SET status = 'past_due', updated_at = NOW()
FROM purser.billing_tiers tier
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tier.id = subscription.tier_id
  AND NOT COALESCE(tier.is_default_postpaid, false)
  AND subscription.billing_model = 'postpaid'
  AND subscription.status IN ('active', 'past_due');

-- name: GetOpenRetainedProviderPaidTerm :one
-- Retained native identities exclude an already-paid fee from Purser invoices.
-- The fallback date can be later than local monthly coordinates during catch-up.
SELECT GREATEST(free_fallback_at, stripe_current_period_end,
                CASE WHEN free_fallback_at IS NULL THEN
                    GREATEST(billing_period_end AT TIME ZONE 'UTC', mollie_next_payment_date::timestamp AT TIME ZONE 'UTC') END)::timestamptz AS paid_until
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_provider_ended_at IS NOT NULL
  AND (NULLIF(stripe_subscription_id, '') IS NOT NULL OR NULLIF(mollie_subscription_id, '') IS NOT NULL)
  AND GREATEST(free_fallback_at, stripe_current_period_end,
               CASE WHEN free_fallback_at IS NULL THEN
                   GREATEST(billing_period_end AT TIME ZONE 'UTC', mollie_next_payment_date::timestamp AT TIME ZONE 'UTC') END) > NOW();

-- name: ClearSubscriptionFreeFallback :execrows
UPDATE purser.tenant_subscriptions
SET status = CASE WHEN status IN ('past_due', 'pending_cancellation') THEN 'active' ELSE status END,
    stripe_subscription_id = CASE WHEN free_fallback_provider_ended_at IS NULL THEN stripe_subscription_id END,
    stripe_subscription_status = CASE WHEN free_fallback_provider_ended_at IS NULL THEN stripe_subscription_status END,
    stripe_current_period_end = CASE WHEN free_fallback_provider_ended_at IS NULL THEN stripe_current_period_end END,
    mollie_subscription_id = CASE WHEN free_fallback_provider_ended_at IS NULL THEN mollie_subscription_id END,
    mollie_next_payment_date = CASE WHEN free_fallback_provider_ended_at IS NULL THEN mollie_next_payment_date END,
    free_fallback_at = NULL, free_fallback_reason = NULL, free_fallback_notified_at = NULL,
    free_fallback_moved_at = NULL, free_fallback_provider_stopping = FALSE,
    free_fallback_provider_ended_at = NULL, free_fallback_wait_since = NULL,
    free_fallback_attempted_at = NULL, free_fallback_failures = 0, free_fallback_error = NULL,
    free_fallback_notice_attempted_at = NULL, free_fallback_notice_failures = 0,
    free_fallback_notice_error = NULL, free_fallback_notice_abandoned_at = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (free_fallback_at IS NOT NULL OR free_fallback_provider_ended_at IS NOT NULL);

-- name: ListDueFreeFallbacks :many
-- Snapshot the due cohort once, then fetch exclusion keys in batches. Claims
-- still recheck every exclusion under the subscription lock.
WITH candidates AS MATERIALIZED (
    SELECT subscription.id, subscription.tenant_id, subscription.free_fallback_attempted_at,
           subscription.free_fallback_at
    FROM purser.tenant_subscriptions subscription
    WHERE subscription.free_fallback_at <= sqlc.arg(now)::timestamptz
      AND subscription.free_fallback_moved_at IS NULL
      AND (subscription.free_fallback_attempted_at IS NULL
           OR subscription.free_fallback_attempted_at <= NOW() - INTERVAL '5 minutes')
), closed AS MATERIALIZED (
    SELECT closure.tenant_id FROM purser.tenant_billing_closures closure
    WHERE closure.tenant_id = ANY (ARRAY(SELECT candidates.tenant_id FROM candidates))
), granted AS MATERIALIZED (
    SELECT grant_row.subscription_id FROM purser.subscription_operator_grants grant_row
    WHERE grant_row.subscription_id = ANY (ARRAY(SELECT candidates.id FROM candidates))
)
SELECT candidates.tenant_id::text AS tenant_id
FROM candidates
WHERE candidates.tenant_id NOT IN (SELECT closed.tenant_id FROM closed)
  AND candidates.id NOT IN (SELECT granted.subscription_id FROM granted)
ORDER BY candidates.free_fallback_attempted_at ASC NULLS FIRST,
         candidates.free_fallback_at ASC, candidates.tenant_id ASC
LIMIT 100;

-- name: MoveSubscriptionToFreeFallback :execrows
-- A checkout staged during the grace period keeps its pending state, so its
-- settlement can still activate the paid tier.
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(tier_id)::text::uuid, status = 'active', billing_model = 'postpaid',
    stripe_subscription_id = NULL, stripe_subscription_status = NULL, stripe_current_period_end = NULL,
    mollie_subscription_id = NULL, mollie_next_payment_date = NULL, cancelled_at = NULL,
    pending_tier_id = CASE WHEN pending_reason = 'stripe_checkout' THEN pending_tier_id END,
    pending_effective_at = CASE WHEN pending_reason = 'stripe_checkout' THEN pending_effective_at END,
    pending_reason = CASE WHEN pending_reason = 'stripe_checkout' THEN pending_reason END,
    free_fallback_provider_ended_at = COALESCE(free_fallback_provider_ended_at, NOW()),
    free_fallback_moved_at = NOW(), free_fallback_provider_stopping = FALSE, free_fallback_wait_since = NULL, free_fallback_error = NULL, free_fallback_failures = 0,
    free_fallback_notice_attempted_at = NULL, free_fallback_notice_error = NULL, free_fallback_notice_failures = 0,
    free_fallback_notice_abandoned_at = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at <= NOW()
  AND free_fallback_moved_at IS NULL;

-- name: ListFreeFallbackNotices :many
SELECT subscription.tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions subscription
WHERE free_fallback_at IS NOT NULL
  AND (free_fallback_moved_at IS NOT NULL OR free_fallback_notified_at IS NULL)
  AND free_fallback_notice_abandoned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row
      WHERE grant_row.subscription_id = subscription.id)
  AND (free_fallback_notice_attempted_at IS NULL OR free_fallback_notice_attempted_at <= NOW() - (LEAST(720, 5 * power(2, LEAST(free_fallback_notice_failures, 8))) * INTERVAL '1 minute'))
ORDER BY free_fallback_notice_attempted_at ASC NULLS FIRST, free_fallback_at ASC, subscription.tenant_id ASC
LIMIT 100;

-- name: ClaimFreeFallbackNotice :one
-- The claim is committed before SMTP; no billing row lock spans external I/O.
UPDATE purser.tenant_subscriptions subscription
SET free_fallback_notice_attempted_at = NOW(),
    free_fallback_notice_failures = free_fallback_notice_failures + 1
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at IS NOT NULL
  AND (free_fallback_moved_at IS NOT NULL OR free_fallback_notified_at IS NULL)
  AND free_fallback_notice_abandoned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row
      WHERE grant_row.subscription_id = subscription.id)
  AND (free_fallback_notice_attempted_at IS NULL OR free_fallback_notice_attempted_at <= NOW() - (LEAST(720, 5 * power(2, LEAST(free_fallback_notice_failures, 8))) * INTERVAL '1 minute'))
RETURNING COALESCE(billing_email, '')::text AS billing_email, COALESCE(billing_name, '')::text AS billing_name,
          free_fallback_at::timestamptz AS free_fallback_at, COALESCE(free_fallback_reason, '')::text AS free_fallback_reason,
          (free_fallback_moved_at IS NOT NULL)::boolean AS moved,
          free_fallback_notice_attempted_at::timestamptz AS attempted_at;

-- name: CompleteFreeFallbackNotice :exec
-- A stale sender cannot acknowledge a newer lifecycle or its move notice.
UPDATE purser.tenant_subscriptions
SET free_fallback_at = CASE WHEN sqlc.arg(moved)::boolean THEN NULL ELSE free_fallback_at END,
    free_fallback_reason = CASE WHEN sqlc.arg(moved)::boolean THEN NULL ELSE free_fallback_reason END,
    free_fallback_moved_at = CASE WHEN sqlc.arg(moved)::boolean THEN NULL ELSE free_fallback_moved_at END,
    free_fallback_notified_at = CASE WHEN sqlc.arg(moved)::boolean THEN NULL ELSE NOW() END,
    free_fallback_notice_error = NULL, free_fallback_notice_failures = 0, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_notice_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND (free_fallback_moved_at IS NOT NULL) = sqlc.arg(moved)::boolean;

-- name: RecordFreeFallbackNoticeFailure :exec
UPDATE purser.tenant_subscriptions
SET free_fallback_notice_error = sqlc.arg(failure)::text,
    free_fallback_notice_abandoned_at = CASE WHEN free_fallback_notice_failures >= 5
        THEN NOW() ELSE free_fallback_notice_abandoned_at END
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_notice_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz;

-- name: TenantOwesInvoicesOnFree :one
-- Settling first applies to a tenant on Free. A finalized invoice is owed until
-- confirmed payments cover it.
SELECT EXISTS (
    SELECT 1
    FROM purser.tenant_subscriptions subscription
    JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
    JOIN purser.billing_invoices invoice ON invoice.tenant_id = subscription.tenant_id
    WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND COALESCE(tier.is_default_postpaid, false) AND tier.base_price = 0
      AND invoice.document_kind = 'invoice'
      AND invoice.status IN ('pending', 'overdue', 'failed')
      AND invoice.amount > COALESCE((
          SELECT SUM(payment.amount - (COALESCE(payment.reversed_amount_cents, 0)::numeric / 100))
          FROM purser.billing_payments payment
          WHERE payment.invoice_id = invoice.id
            AND payment.status = 'confirmed'
            AND payment.currency = invoice.currency
      ), 0)
)::boolean AS owes;

-- name: GetSubscriptionFreeFallback :one
-- Pricing boundaries cannot precede a phase already opened by an operator.
SELECT GREATEST(free_fallback_at, billing_period_start AT TIME ZONE 'UTC')::timestamptz AS cutoff,
       free_fallback_at::timestamptz AS scheduled_at, free_fallback_reason,
       COALESCE(stripe_subscription_id, '')::text AS stripe_subscription_id,
       COALESCE(stripe_subscription_status, '')::text AS stripe_subscription_status,
       COALESCE(stripe_customer_id, '')::text AS stripe_customer_id, tier_id::text AS tier_id,
       COALESCE(payment_method, '')::text AS payment_method, free_fallback_provider_ended_at,
       COALESCE(billing_period_end AT TIME ZONE 'UTC', free_fallback_at)::timestamptz AS period_end,
       COALESCE(mollie_subscription_id, '')::text AS mollie_subscription_id,
       COALESCE((SELECT funding.provider_payment_id
           FROM purser.payment_provider_intents native
           JOIN purser.payment_provider_intents funding ON funding.tenant_id = native.tenant_id
             AND funding.provider = 'mollie' AND funding.purpose = 'mollie_first_payment'
             AND funding.status = 'succeeded' AND funding.provider_payment_id IS NOT NULL
             AND funding.provider_customer_id = native.provider_customer_id
             AND funding.local_reference_id = native.local_reference_id
             AND funding.created_at <= native.created_at
           WHERE native.tenant_id = subscription.tenant_id AND native.provider = 'mollie'
             AND native.purpose = 'mollie_subscription_create'
             AND native.provider_subscription_id = subscription.mollie_subscription_id
             AND NOT EXISTS (
                 SELECT 1 FROM purser.payment_provider_intents prior
                 WHERE prior.tenant_id = native.tenant_id AND prior.provider = 'mollie'
                   AND prior.purpose = 'mollie_subscription_create'
                   AND prior.provider_subscription_id IS NOT NULL
                   AND (prior.succeeded_at IS NOT NULL OR prior.status IN ('succeeded', 'provider_open'))
                   AND prior.id <> native.id
                   AND prior.created_at >= funding.created_at AND prior.created_at < native.created_at
             )
           ORDER BY funding.created_at DESC LIMIT 1), '')::text AS mollie_initial_payment_id,
       COALESCE((SELECT MAX(invoice.base_fee_period_end) FROM purser.billing_invoices invoice
           WHERE invoice.tenant_id = subscription.tenant_id AND invoice.status = 'paid'
             AND invoice.document_kind = 'invoice' AND invoice.base_amount > 0
             AND invoice.usage_details#>>'{tier_info,tier_id}' = subscription.tier_id::text
             AND invoice.usage_details->'unused_share_returned' IS NULL
             AND invoice.amount <= COALESCE((SELECT SUM(payment.amount - COALESCE(payment.reversed_amount_cents, 0)::numeric / 100)
                 FROM purser.billing_payments payment WHERE payment.invoice_id = invoice.id AND payment.status = 'confirmed'
                   AND payment.currency = invoice.currency), 0)), 'epoch'::timestamptz)::timestamptz AS paid_advance_until,
       EXISTS (SELECT 1 FROM purser.billing_invoices invoice JOIN purser.billing_payments payment ON payment.invoice_id = invoice.id
           WHERE invoice.tenant_id = subscription.tenant_id AND invoice.base_fee_period_end >= subscription.free_fallback_at
             AND payment.status = 'pending')::boolean AS advance_payment_pending,
       COALESCE((SELECT customer.mollie_customer_id FROM purser.mollie_customers customer WHERE customer.tenant_id = subscription.tenant_id), '')::text AS mollie_customer_id
FROM purser.tenant_subscriptions subscription
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at <= NOW()
  AND free_fallback_moved_at IS NULL;

-- name: ClaimFreeFallbackMove :one
-- Five minutes bounds duplicate work and defers failed rows behind untouched work.
UPDATE purser.tenant_subscriptions subscription
SET free_fallback_attempted_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at <= NOW() AND free_fallback_moved_at IS NULL
  AND (free_fallback_attempted_at IS NULL OR free_fallback_attempted_at <= NOW() - INTERVAL '5 minutes')
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = subscription.tenant_id)
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id = subscription.id)
RETURNING free_fallback_attempted_at::timestamptz AS attempted_at;

-- name: RecordFreeFallbackMoveFailure :exec
UPDATE purser.tenant_subscriptions
SET free_fallback_error = sqlc.arg(failure)::text, free_fallback_failures = free_fallback_failures + 1
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND free_fallback_moved_at IS NULL;

-- name: CountRepeatedFreeFallbackFailures :one
-- Exact service-wide counts over indexed failure cohorts. Exclusion keys are
-- fetched once per cohort instead of once for every aggregate input row.
WITH moves AS MATERIALIZED (
    SELECT id, tenant_id FROM purser.tenant_subscriptions
    WHERE free_fallback_at IS NOT NULL AND free_fallback_moved_at IS NULL
      AND free_fallback_error IS NOT NULL AND free_fallback_failures > 1
    UNION
    SELECT id, tenant_id FROM purser.tenant_subscriptions
    WHERE free_fallback_at IS NOT NULL AND free_fallback_moved_at IS NULL
      AND free_fallback_wait_since <= NOW() - INTERVAL '14 days'
), notices AS MATERIALIZED (
    SELECT id, tenant_id FROM purser.tenant_subscriptions
    WHERE free_fallback_at IS NOT NULL AND free_fallback_notice_error IS NOT NULL
      AND free_fallback_notice_failures > 1 AND free_fallback_notice_abandoned_at IS NULL
), closed AS MATERIALIZED (
    SELECT closure.tenant_id FROM purser.tenant_billing_closures closure
    WHERE closure.tenant_id = ANY (ARRAY(
        SELECT moves.tenant_id FROM moves UNION SELECT notices.tenant_id FROM notices
    ))
), granted AS MATERIALIZED (
    SELECT grant_row.subscription_id FROM purser.subscription_operator_grants grant_row
    WHERE grant_row.subscription_id = ANY (ARRAY(SELECT moves.id FROM moves UNION SELECT notices.id FROM notices))
)
SELECT (SELECT COUNT(*) FROM moves
        WHERE moves.tenant_id NOT IN (SELECT closed.tenant_id FROM closed)
          AND moves.id NOT IN (SELECT granted.subscription_id FROM granted))::bigint AS moves,
       (SELECT COUNT(*) FROM notices
        WHERE notices.tenant_id NOT IN (SELECT closed.tenant_id FROM closed)
          AND notices.id NOT IN (SELECT granted.subscription_id FROM granted))::bigint AS notices;

-- name: StartFreeFallbackProviderStop :execrows
UPDATE purser.tenant_subscriptions
SET free_fallback_provider_stopping = TRUE, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND free_fallback_moved_at IS NULL;

-- name: RecoverFreeFallbackProvider :execrows
-- Recovery keeps every missed monthly phase until invoice finalization advances it.
UPDATE purser.tenant_subscriptions
SET free_fallback_unbilled_through = GREATEST(free_fallback_unbilled_through, NOW()),
    free_fallback_provider_ended_at = NULL, status = 'active', free_fallback_at = NULL, free_fallback_reason = NULL,
    free_fallback_notified_at = NULL, free_fallback_moved_at = NULL,
    free_fallback_provider_stopping = FALSE, free_fallback_wait_since = NULL,
    free_fallback_error = NULL, free_fallback_failures = 0,
    free_fallback_attempted_at = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_provider_stopping;

-- name: RetainPaidFreeFallbackPeriod :execrows
-- A payment completed while the native cancellation ran still buys its full term.
UPDATE purser.tenant_subscriptions
SET free_fallback_provider_ended_at = COALESCE(free_fallback_provider_ended_at, NOW()),
    status = 'pending_cancellation', free_fallback_at = sqlc.arg(paid_until)::timestamptz,
    free_fallback_reason = 'cancelled', free_fallback_notified_at = NOW(),
    free_fallback_provider_stopping = FALSE, free_fallback_wait_since = NULL,
    free_fallback_error = NULL, free_fallback_failures = 0, free_fallback_attempted_at = NULL,
    free_fallback_unbilled_through = GREATEST(free_fallback_unbilled_through, NOW()), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_provider_stopping;

-- name: WaitFreeFallbackSettlement :exec
UPDATE purser.tenant_subscriptions
SET free_fallback_wait_since = COALESCE(free_fallback_wait_since, NOW()),
    free_fallback_failures = 0, free_fallback_error = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_provider_stopping;

-- name: CompleteFreeFallbackPeriod :execrows
-- A successful monthly catch-up keeps its provider fence and bounded retry claim.
UPDATE purser.tenant_subscriptions
SET free_fallback_error = NULL, free_fallback_failures = 0, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_at = sqlc.arg(scheduled_at)::timestamptz
  AND free_fallback_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND free_fallback_provider_stopping;

-- name: GetFreeFallbackTier :one
SELECT id::text AS id
FROM purser.billing_tiers
WHERE COALESCE(is_default_postpaid, false) AND COALESCE(is_active, true)
  AND base_price = 0
ORDER BY id
LIMIT 1
FOR SHARE;

-- name: AcknowledgeFreeFallbackHold :exec
-- A newer claim or lifecycle cannot be cleared by an older worker.
UPDATE purser.tenant_subscriptions
SET free_fallback_error = NULL, free_fallback_failures = 0
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND free_fallback_attempted_at = sqlc.arg(attempted_at)::timestamptz
  AND free_fallback_moved_at IS NULL;
