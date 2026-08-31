-- name: GetPaymentStatusEmailDetails :one
SELECT invoice.tenant_id::text AS tenant_id, invoice.amount::float8 AS amount,
       invoice.currency, subscription.billing_email
FROM purser.billing_invoices invoice
JOIN purser.tenant_subscriptions subscription ON invoice.tenant_id = subscription.tenant_id
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid;

-- name: ClaimWebhookEvent :one
WITH claimed AS (
    INSERT INTO purser.webhook_events (
        provider, event_id, event_type, status, signature_header, raw_payload, received_at
    ) VALUES (
        sqlc.arg(provider), sqlc.arg(event_id), sqlc.arg(event_type), 'claimed',
        NULLIF(sqlc.arg(signature_header), ''), sqlc.arg(raw_payload), NOW()
    )
    ON CONFLICT (provider, event_id) DO UPDATE
    SET status = 'claimed',
        retry_count = purser.webhook_events.retry_count + 1,
        received_at = NOW(),
        event_type = COALESCE(NULLIF(EXCLUDED.event_type, ''), purser.webhook_events.event_type),
        signature_header = COALESCE(EXCLUDED.signature_header, purser.webhook_events.signature_header),
        raw_payload = COALESCE(EXCLUDED.raw_payload, purser.webhook_events.raw_payload),
        last_error = NULL
    WHERE purser.webhook_events.status IN ('failed_retryable', 'blocked')
       OR (purser.webhook_events.status = 'claimed'
           AND purser.webhook_events.received_at < NOW() - (sqlc.arg(lease_seconds)::int * INTERVAL '1 second'))
    RETURNING status
)
SELECT status, TRUE AS acquired FROM claimed
UNION ALL
SELECT status, FALSE AS acquired
FROM purser.webhook_events
WHERE provider = sqlc.arg(provider) AND event_id = sqlc.arg(event_id)
  AND NOT EXISTS (SELECT 1 FROM claimed)
LIMIT 1;

-- name: MarkWebhookEventSucceeded :execrows
UPDATE purser.webhook_events
SET status = 'processed', processed_at = NOW(), last_error = NULL,
    provider_object_id = COALESCE(provider_object_id, NULLIF(sqlc.arg(provider_object_id), ''))
WHERE provider = sqlc.arg(provider) AND event_id = sqlc.arg(event_id);

-- name: MarkWebhookEventFailed :execrows
UPDATE purser.webhook_events
SET status = sqlc.arg(status)::varchar,
    last_error = sqlc.arg(last_error),
    processed_at = CASE WHEN sqlc.arg(terminal)::boolean THEN NOW() ELSE processed_at END
WHERE provider = sqlc.arg(provider) AND event_id = sqlc.arg(event_id);

-- name: GetStripeInvoiceCardPayment :one
SELECT payment.id::text AS payment_id, invoice.tenant_id::text AS tenant_id,
       (payment.amount * 100)::bigint AS amount_cents, payment.currency
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON payment.invoice_id = invoice.id
WHERE payment.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND payment.method = 'card'
  AND payment.tx_id = sqlc.arg(transaction_id)
ORDER BY payment.created_at DESC
LIMIT 1
FOR UPDATE OF payment;

-- name: GetTenantByStripeSubscription :one
SELECT tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions
WHERE stripe_subscription_id = sqlc.arg(stripe_subscription_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: GetTenantByStripeCustomer :one
SELECT tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions
WHERE stripe_customer_id = sqlc.arg(stripe_customer_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: MarkStripeSubscriptionIntentSucceeded :exec
UPDATE purser.payment_provider_intents
SET provider_subscription_id = COALESCE(provider_subscription_id, NULLIF(sqlc.arg(subscription_id), '')),
    status = 'succeeded', succeeded_at = COALESCE(succeeded_at, NOW()), updated_at = NOW()
WHERE provider = 'stripe' AND provider_subscription_id = sqlc.arg(subscription_id);

-- name: UpdateTenantStripeSubscriptionStatus :execrows
UPDATE purser.tenant_subscriptions
SET stripe_subscription_status = sqlc.arg(stripe_status),
    status = sqlc.arg(status),
    stripe_current_period_end = sqlc.narg(period_end)::timestamptz,
    billing_period_start = COALESCE(sqlc.narg(period_start)::timestamp, billing_period_start),
    billing_period_end = COALESCE(sqlc.narg(period_end)::timestamp, billing_period_end),
    next_billing_date = COALESCE(sqlc.narg(period_end)::timestamp, next_billing_date),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetInternalSubscriptionID :one
SELECT id::text AS id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: ResetTenantDunningAttempts :exec
UPDATE purser.tenant_subscriptions
SET dunning_attempts = 0, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: IncrementTenantDunningAttempts :exec
UPDATE purser.tenant_subscriptions
SET dunning_attempts = dunning_attempts + 1, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetClusterSubscriptionByStripeID :one
SELECT cluster_id, tenant_id::text AS tenant_id
FROM purser.cluster_subscriptions
WHERE stripe_subscription_id = sqlc.arg(stripe_subscription_id);

-- name: UpdateClusterStripeSubscriptionStatus :execrows
UPDATE purser.cluster_subscriptions
SET stripe_subscription_status = sqlc.arg(stripe_status),
    status = sqlc.arg(status),
    stripe_current_period_end = sqlc.narg(period_end),
    updated_at = NOW()
WHERE stripe_subscription_id = sqlc.arg(stripe_subscription_id);

-- name: AttachAndUpdateClusterStripeSubscription :execrows
UPDATE purser.cluster_subscriptions
SET stripe_subscription_id = sqlc.arg(stripe_subscription_id),
    stripe_subscription_status = sqlc.arg(stripe_status),
    status = sqlc.arg(status),
    stripe_current_period_end = sqlc.narg(period_end),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND cluster_id = sqlc.arg(cluster_id);

-- name: UpsertMollieCustomer :exec
INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(mollie_customer_id))
ON CONFLICT (tenant_id) DO UPDATE
SET mollie_customer_id = EXCLUDED.mollie_customer_id;

-- name: GetTenantByMollieSubscription :one
SELECT tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions
WHERE mollie_subscription_id = sqlc.arg(mollie_subscription_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: InsertMollieSubscriptionPayment :exec
INSERT INTO purser.billing_payments (
    invoice_id, method, amount, currency, tx_id, status, created_at, updated_at
) VALUES (
    sqlc.arg(invoice_id)::text::uuid, 'card', sqlc.arg(amount)::numeric,
    sqlc.arg(currency), sqlc.arg(transaction_id), 'pending', NOW(), NOW()
)
ON CONFLICT DO NOTHING;

-- name: UpdateMollieNextPaymentDate :exec
UPDATE purser.tenant_subscriptions
SET mollie_next_payment_date = sqlc.arg(next_payment_date)::text::date, updated_at = NOW()
WHERE mollie_subscription_id = sqlc.arg(mollie_subscription_id);

-- name: AttachMollieBillingPayment :execrows
UPDATE purser.billing_payments
SET tx_id = sqlc.arg(transaction_id), updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid
  AND status = 'pending'
  AND (tx_id IS NULL OR tx_id = sqlc.arg(transaction_id) OR tx_id LIKE 'mollie-overage-intent:%');

-- name: GetBillingInvoiceTenant :one
SELECT tenant_id::text AS tenant_id
FROM purser.billing_invoices
WHERE id = sqlc.arg(invoice_id)::text::uuid;

-- name: GetStripePaymentMappingByIntent :one
SELECT invoice.tenant_id::text AS tenant_id, payment.id::text AS payment_id
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.tx_id = sqlc.arg(payment_intent_id)
  AND payment.method = 'card'
ORDER BY payment.created_at DESC
LIMIT 1
FOR UPDATE OF payment;

-- name: GetStripePaymentIntentForCharge :one
SELECT COALESCE(MAX(metadata->>'payment_intent_id'), '')::text AS payment_intent_id
FROM purser.provider_payment_objects
WHERE provider = 'stripe'
  AND object_type = 'charge'
  AND provider_object_id = sqlc.arg(charge_id);

-- name: InsertPendingStripeDispute :exec
INSERT INTO purser.payment_reversals (
    tenant_id, payment_id, provider, reversal_type,
    provider_reversal_id, provider_charge_id,
    amount_cents, currency, status, reason
)
SELECT invoice.tenant_id, payment.id, 'stripe', 'dispute',
       sqlc.arg(dispute_id), sqlc.arg(charge_id), sqlc.arg(amount_cents),
       sqlc.arg(currency), 'pending', sqlc.arg(reason)
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON payment.invoice_id = invoice.id
WHERE payment.tx_id = sqlc.arg(payment_intent_id)
  AND payment.method = 'card'
ORDER BY payment.created_at DESC
LIMIT 1
ON CONFLICT (provider, provider_reversal_id) DO NOTHING;

-- name: MarkStripeDisputeNeedsReview :execrows
UPDATE purser.payment_reversals
SET status = 'needs_review', operator_review_required = TRUE, updated_at = NOW()
WHERE provider = 'stripe'
  AND provider_reversal_id = sqlc.arg(dispute_id);

-- name: GetInvoicePaymentForReversal :one
SELECT payment.id::text AS payment_id, payment.invoice_id::text AS invoice_id,
       invoice.tenant_id::text AS tenant_id, payment.currency
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON payment.invoice_id = invoice.id
WHERE payment.method = 'card' AND payment.tx_id = sqlc.arg(provider_payment_id)
ORDER BY payment.created_at DESC
LIMIT 1;

-- name: GetPendingTopupForReversal :one
SELECT id::text AS topup_id, tenant_id::text AS tenant_id, currency
FROM purser.pending_topups
WHERE provider_payment_id = sqlc.arg(provider_payment_id)
   OR checkout_id = sqlc.arg(provider_payment_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: UpsertSucceededPaymentReversal :one
INSERT INTO purser.payment_reversals (
    tenant_id, payment_id, pending_topup_id, invoice_id,
    provider, reversal_type, provider_reversal_id, provider_charge_id,
    amount_cents, currency, status, reason
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.narg(payment_id)::text::uuid,
    sqlc.narg(pending_topup_id)::text::uuid, sqlc.narg(invoice_id)::text::uuid,
    sqlc.arg(provider), sqlc.arg(reversal_type), sqlc.arg(provider_reversal_id),
    sqlc.narg(provider_charge_id), sqlc.arg(amount_cents), sqlc.arg(currency),
    'succeeded', sqlc.narg(reason)
)
ON CONFLICT (provider, provider_reversal_id) DO UPDATE SET
    payment_id = COALESCE(purser.payment_reversals.payment_id, EXCLUDED.payment_id),
    pending_topup_id = COALESCE(purser.payment_reversals.pending_topup_id, EXCLUDED.pending_topup_id),
    invoice_id = COALESCE(purser.payment_reversals.invoice_id, EXCLUDED.invoice_id),
    provider_charge_id = COALESCE(purser.payment_reversals.provider_charge_id, EXCLUDED.provider_charge_id),
    status = 'succeeded', updated_at = NOW()
WHERE purser.payment_reversals.status = 'pending'
RETURNING id::text AS id;

-- name: AddBillingPaymentReversedAmount :exec
UPDATE purser.billing_payments
SET reversed_amount_cents = reversed_amount_cents + sqlc.arg(amount_cents), updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid;

-- name: AddBillingInvoiceReversedAmount :exec
UPDATE purser.billing_invoices
SET reversed_paid_cents = reversed_paid_cents + sqlc.arg(amount_cents), updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid;

-- name: ReopenUnderpaidBillingInvoice :execrows
UPDATE purser.billing_invoices invoice
SET status = 'pending', reopened_at = NOW(), updated_at = NOW()
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.status = 'paid'
  AND invoice.currency = sqlc.arg(currency)
  AND (
      SELECT COALESCE(SUM(payment.amount - COALESCE(payment.reversed_amount_cents, 0)::numeric / 100), 0)
      FROM purser.billing_payments payment
      WHERE payment.invoice_id = invoice.id
        AND payment.status = 'confirmed'
        AND payment.currency = invoice.currency
  ) < invoice.amount;

-- name: GetBillingInvoiceAmountCents :one
SELECT (amount * 100)::bigint AS amount_cents
FROM purser.billing_invoices
WHERE id = sqlc.arg(invoice_id)::text::uuid;

-- name: ListOperatorAccrualsForInvoice :many
SELECT id::text AS id, cluster_owner_tenant_id::text AS owner_tenant_id,
       cluster_id, currency, gross_cents, platform_fee_cents, payable_cents,
       period_start, period_end
FROM purser.operator_credit_ledger
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND entry_type = 'accrual';

-- name: UpsertOperatorCreditClawback :one
WITH existing AS (
    SELECT operator_credit_ledger_id AS id
    FROM purser.operator_credit_clawback_reversals
    WHERE payment_reversal_id = sqlc.arg(payment_reversal_id)::text::uuid
      AND accrual_ledger_id = sqlc.arg(accrual_ledger_id)::text::uuid
), inserted AS (
    INSERT INTO purser.operator_credit_ledger (
        source_type, invoice_line_item_id, provider_usage_record_id, usage_adjustment_id,
        stripe_invoice_id, entry_type, reverses_ledger_id, cluster_owner_tenant_id,
        cluster_id, invoice_id, period_start, period_end, currency, gross_cents,
        platform_fee_cents, payable_cents, status, notes
    )
    SELECT ledger.source_type, ledger.invoice_line_item_id, ledger.provider_usage_record_id,
           ledger.usage_adjustment_id, ledger.stripe_invoice_id, 'clawback', ledger.id,
           ledger.cluster_owner_tenant_id, ledger.cluster_id, ledger.invoice_id,
           ledger.period_start, ledger.period_end, ledger.currency,
           -(sqlc.arg(gross_cents)::bigint), -(sqlc.arg(fee_cents)::bigint),
           -(sqlc.arg(payable_cents)::bigint),
           'clawed_back', jsonb_build_object('payment_reversal_id', sqlc.arg(payment_reversal_id)::text)
    FROM purser.operator_credit_ledger ledger
    WHERE ledger.id = sqlc.arg(accrual_ledger_id)::text::uuid
      AND NOT EXISTS (SELECT 1 FROM existing)
    RETURNING id
), chosen AS (
    SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1
), mapped AS (
    INSERT INTO purser.operator_credit_clawback_reversals (
        payment_reversal_id, operator_credit_ledger_id, accrual_ledger_id
    )
    SELECT sqlc.arg(payment_reversal_id)::text::uuid, id,
           sqlc.arg(accrual_ledger_id)::text::uuid
    FROM chosen
    ON CONFLICT (payment_reversal_id, accrual_ledger_id) DO UPDATE
    SET operator_credit_ledger_id = EXCLUDED.operator_credit_ledger_id
    RETURNING operator_credit_ledger_id
)
SELECT operator_credit_ledger_id::text AS operator_credit_ledger_id FROM mapped;

-- name: MarkOperatorAccrualClawedBack :exec
UPDATE purser.operator_credit_ledger
SET status = 'clawed_back', updated_at = NOW()
WHERE id = sqlc.arg(accrual_ledger_id)::text::uuid
  AND status IN ('held', 'accruing', 'eligible');

-- name: LinkPaymentReversalToOperatorCredit :exec
UPDATE purser.payment_reversals
SET operator_credit_ledger_id = sqlc.arg(operator_credit_ledger_id)::text::uuid, updated_at = NOW()
WHERE id = sqlc.arg(payment_reversal_id)::text::uuid;

-- name: AddPendingTopupRefundedAmount :exec
UPDATE purser.pending_topups
SET refunded_amount_cents = refunded_amount_cents + sqlc.arg(amount_cents), updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid;

-- name: InsertPrepaidTopupReversalTransaction :exec
INSERT INTO purser.balance_transactions (
    tenant_id, amount_cents, balance_after_cents, transaction_type,
    description, reference_id, reference_type, actor_kind, reason
)
SELECT sqlc.arg(tenant_id)::text::uuid, -(sqlc.arg(amount_cents)::bigint),
       COALESCE((
           SELECT balance_cents FROM purser.prepaid_balances
           WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND currency = sqlc.arg(currency)
       ), 0) - sqlc.arg(amount_cents),
       'refund', sqlc.arg(description), sqlc.arg(reversal_id)::text::uuid,
       'payment_reversal', 'webhook', sqlc.arg(reason)
ON CONFLICT (tenant_id, reference_type, reference_id)
WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO NOTHING;

-- name: SubtractPrepaidBalance :execrows
UPDATE purser.prepaid_balances
SET balance_cents = balance_cents - sqlc.arg(amount_cents), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND currency = sqlc.arg(currency);

-- name: MarkPaymentReversalForReview :exec
UPDATE purser.payment_reversals
SET operator_review_required = TRUE, updated_at = NOW()
WHERE id = sqlc.arg(reversal_id)::text::uuid;

-- name: UpsertProviderPaymentObject :exec
INSERT INTO purser.provider_payment_objects (
    provider, object_type, provider_object_id, tenant_id,
    local_reference_type, local_reference_id, intent_id, metadata,
    created_at, updated_at
) VALUES (
    sqlc.arg(provider), sqlc.arg(object_type), sqlc.arg(provider_object_id),
    sqlc.narg(tenant_id)::text::uuid, sqlc.narg(local_reference_type),
    sqlc.narg(local_reference_id)::text::uuid, sqlc.narg(intent_id)::text::uuid,
    sqlc.arg(metadata)::jsonb, NOW(), NOW()
)
ON CONFLICT (provider, object_type, provider_object_id) DO UPDATE SET
    tenant_id = COALESCE(EXCLUDED.tenant_id, purser.provider_payment_objects.tenant_id),
    local_reference_type = COALESCE(EXCLUDED.local_reference_type, purser.provider_payment_objects.local_reference_type),
    local_reference_id = COALESCE(EXCLUDED.local_reference_id, purser.provider_payment_objects.local_reference_id),
    intent_id = COALESCE(EXCLUDED.intent_id, purser.provider_payment_objects.intent_id),
    metadata = purser.provider_payment_objects.metadata || EXCLUDED.metadata,
    updated_at = NOW();

-- name: GetMollieAppliedReversalCents :one
SELECT COALESCE(SUM(amount_cents), 0)::bigint AS amount_cents
FROM purser.payment_reversals
WHERE provider = 'mollie' AND reversal_type = sqlc.arg(reversal_type)
  AND provider_reversal_id LIKE sqlc.arg(provider_reversal_prefix)
  AND status = 'succeeded';

-- name: UpsertMolliePaymentObservation :exec
INSERT INTO purser.mollie_payment_observations (
    tenant_id, mollie_payment_id, mollie_subscription_id, mollie_mandate_id,
    sequence_type, status, amount_cents, currency, paid_at, raw_payload
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(mollie_payment_id),
    sqlc.arg(mollie_subscription_id), sqlc.arg(mollie_mandate_id),
    sqlc.arg(sequence_type), sqlc.arg(status), sqlc.arg(amount_cents),
    sqlc.arg(currency), sqlc.narg(paid_at), sqlc.arg(raw_payload)
)
ON CONFLICT (mollie_payment_id) DO UPDATE SET
    status = EXCLUDED.status, amount_cents = EXCLUDED.amount_cents,
    currency = EXCLUDED.currency, paid_at = EXCLUDED.paid_at,
    attempt_count = purser.mollie_payment_observations.attempt_count + 1,
    updated_at = NOW();

-- name: GetMollieObservationDrainInvoice :one
SELECT invoice.tenant_id::text AS tenant_id,
       COALESCE(subscription.mollie_subscription_id, '')::text AS mollie_subscription_id,
       invoice.currency, invoice.period_start, invoice.period_end
FROM purser.billing_invoices invoice
JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid;

-- name: ListMolliePaymentObservationsForInvoice :many
SELECT mollie_payment_id, status, amount_cents, currency, paid_at
FROM purser.mollie_payment_observations
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND mollie_subscription_id = sqlc.arg(mollie_subscription_id)
  AND resolved_at IS NULL
  AND (sqlc.narg(period_start)::timestamptz IS NULL OR paid_at IS NULL OR paid_at >= sqlc.narg(period_start))
  AND (sqlc.narg(period_end)::timestamptz IS NULL OR paid_at IS NULL OR paid_at <= sqlc.narg(period_end))
ORDER BY created_at ASC;

-- name: ResolveMolliePaymentObservation :exec
UPDATE purser.mollie_payment_observations
SET resolved_at = NOW(), resolution = 'attached',
    invoice_id = sqlc.arg(invoice_id)::text::uuid, updated_at = NOW()
WHERE mollie_payment_id = sqlc.arg(mollie_payment_id);

-- name: ResolveMollieSubscriptionInvoice :one
SELECT invoice.id::text AS invoice_id
FROM purser.billing_invoices invoice
JOIN purser.tenant_subscriptions subscription ON subscription.tenant_id = invoice.tenant_id
WHERE subscription.mollie_subscription_id = sqlc.arg(mollie_subscription_id)
  AND sqlc.arg(payment_created_at)::timestamptz::date >= invoice.period_start::date
  AND sqlc.arg(payment_created_at)::timestamptz::date <= invoice.period_end::date
  AND invoice.status IN ('pending', 'overdue')
ORDER BY invoice.created_at DESC
LIMIT 1;

-- name: GetBillingPaymentByProviderTransaction :one
SELECT payment.id::text AS payment_id, payment.invoice_id::text AS invoice_id,
       invoice.tenant_id::text AS tenant_id, payment.amount::text AS amount,
       payment.currency, payment.status
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.tx_id = sqlc.arg(transaction_id) AND payment.method = sqlc.arg(method)
ORDER BY payment.created_at DESC
LIMIT 1;

-- name: GetPendingBillingPaymentForInvoice :one
SELECT payment.id::text AS payment_id, payment.invoice_id::text AS invoice_id,
       invoice.tenant_id::text AS tenant_id, payment.amount::text AS amount,
       payment.currency, payment.status
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND payment.method = sqlc.arg(method) AND payment.status = 'pending' AND payment.tx_id IS NULL
ORDER BY payment.created_at DESC
LIMIT 1;

-- name: UpdateBillingPaymentProviderStatus :exec
UPDATE purser.billing_payments
SET status = sqlc.arg(status), confirmed_at = sqlc.narg(confirmed_at),
    tx_id = COALESCE(NULLIF(tx_id, ''), sqlc.arg(transaction_id)), updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid;

-- name: UpdateBillingPaymentAttemptProviderStatus :exec
UPDATE purser.billing_payment_attempts
SET status = sqlc.arg(status),
    provider_payment_id = COALESCE(provider_payment_id, NULLIF(sqlc.arg(provider_payment_id), '')),
    next_retry_at = NULL, updated_at = NOW()
WHERE payment_id = sqlc.arg(payment_id)::text::uuid AND provider = sqlc.arg(provider);

-- name: MarkFullySettledBillingInvoicePaid :execrows
UPDATE purser.billing_invoices invoice
SET status = 'paid', paid_at = COALESCE(invoice.paid_at, sqlc.arg(paid_at)), updated_at = NOW()
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.status IN ('pending', 'overdue')
  AND (
      SELECT COALESCE(SUM(payment.amount - COALESCE(payment.reversed_amount_cents, 0)::numeric / 100), 0)
      FROM purser.billing_payments payment
      WHERE payment.invoice_id = invoice.id
        AND payment.status = 'confirmed'
        AND payment.currency = invoice.currency
  ) >= invoice.amount;

-- name: UpsertMollieMandate :exec
INSERT INTO purser.mollie_mandates (
    tenant_id, mollie_customer_id, mollie_mandate_id,
    status, method, details, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(mollie_customer_id),
    sqlc.arg(mollie_mandate_id), sqlc.arg(status), sqlc.arg(method),
    sqlc.arg(details)::jsonb, sqlc.arg(created_at), NOW()
)
ON CONFLICT (mollie_mandate_id) DO UPDATE SET
    status = EXCLUDED.status, method = EXCLUDED.method,
    details = EXCLUDED.details, updated_at = NOW();
