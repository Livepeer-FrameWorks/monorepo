-- name: InsertUsageReportQuarantine :exec
INSERT INTO purser.usage_report_quarantine (
    report_id, source_id, tenant_id, rejected_reason,
    source_topic, source_partition, source_offset, raw_payload
) VALUES (
    NULLIF(sqlc.arg(report_id)::text, ''), sqlc.arg(source_id), sqlc.narg(tenant_id)::text::uuid,
    sqlc.arg(rejected_reason), sqlc.arg(source_topic), sqlc.arg(source_partition),
    sqlc.arg(source_offset), sqlc.arg(raw_payload)::jsonb
);

-- name: GetActiveMeterDefinition :one
SELECT unit, allowed_dimensions
FROM purser.meter_definitions
WHERE meter = sqlc.arg(meter) AND active = TRUE;

-- name: GetActiveSubscriptionPeriod :one
SELECT billing_period_start, billing_period_end, mollie_next_payment_date
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status = 'active'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListMollieObservationDrainInvoiceIDs :many
SELECT invoice.id::text AS invoice_id
FROM purser.mollie_payment_observations observation
JOIN purser.billing_invoices invoice ON invoice.tenant_id = observation.tenant_id
WHERE observation.resolved_at IS NULL
  AND observation.mollie_subscription_id IS NOT NULL
  AND invoice.status IN ('pending', 'overdue')
  AND COALESCE(observation.paid_at, observation.created_at) >= invoice.period_start
  AND COALESCE(observation.paid_at, observation.created_at) <= invoice.period_end
GROUP BY invoice.id
ORDER BY invoice.id
LIMIT 100;

-- name: UsageReportExists :one
SELECT EXISTS (
    SELECT 1 FROM purser.usage_reports WHERE report_id = sqlc.arg(report_id)
);

-- name: AdoptLegacyUsageRecord :execrows
UPDATE purser.usage_records
SET unit = sqlc.arg(unit),
    report_id = sqlc.arg(report_id),
    usage_details = sqlc.arg(usage_details)::jsonb,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND cluster_id = sqlc.arg(cluster_id)
  AND source_id = 'legacy'
  AND usage_type = sqlc.arg(usage_type)
  AND period_start = sqlc.arg(period_start)
  AND period_end = sqlc.arg(period_end);

-- name: InsertUsageReportReceipt :exec
INSERT INTO purser.usage_reports (
    report_id, report_kind, source_id, source_region, sequence,
    tenant_id, cluster_id, period_start, period_end, complete
) VALUES (
    sqlc.arg(report_id), sqlc.arg(report_kind), sqlc.arg(source_id),
    sqlc.arg(source_region), sqlc.arg(sequence), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(cluster_id), sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(complete)
)
ON CONFLICT (report_id) DO NOTHING;

-- name: UpsertMeteringSource :one
INSERT INTO purser.metering_sources (source_id, region, active_from, required, updated_at)
VALUES (sqlc.arg(source_id), sqlc.arg(region), sqlc.arg(active_from), TRUE, NOW())
ON CONFLICT (source_id) DO UPDATE SET
	region = CASE
		WHEN purser.metering_sources.region = '' THEN EXCLUDED.region
		ELSE purser.metering_sources.region
	END,
    active_from = LEAST(purser.metering_sources.active_from, EXCLUDED.active_from),
	updated_at = NOW()
RETURNING region;

-- name: UpsertCompletedMeteringWindow :exec
INSERT INTO purser.metering_windows (
    source_id, period_start, period_end, complete, report_count, observed_at
) VALUES (
    sqlc.arg(source_id), sqlc.arg(period_start), sqlc.arg(period_end), TRUE, 1, NOW()
)
ON CONFLICT (source_id, period_start, period_end) DO UPDATE SET
    complete = TRUE,
    report_count = purser.metering_windows.report_count + 1,
    observed_at = NOW();

-- name: UpsertUsageReservation :exec
INSERT INTO purser.usage_reservations (
    tenant_id, source_id, cluster_id, sequence, report_id,
    period_start, period_end, meters, reserved_amount_micro, currency
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(source_id), sqlc.arg(cluster_id),
    sqlc.arg(sequence), sqlc.arg(report_id), sqlc.arg(period_start), sqlc.arg(period_end),
    sqlc.arg(meters)::jsonb, sqlc.arg(reserved_amount_micro), sqlc.arg(currency)
)
ON CONFLICT (tenant_id, source_id, cluster_id) DO UPDATE SET
    sequence = EXCLUDED.sequence, report_id = EXCLUDED.report_id,
    period_start = EXCLUDED.period_start, period_end = EXCLUDED.period_end,
    meters = EXCLUDED.meters, reserved_amount_micro = EXCLUDED.reserved_amount_micro,
    currency = EXCLUDED.currency, updated_at = NOW()
WHERE EXCLUDED.sequence > purser.usage_reservations.sequence;

-- name: GetActiveTenantBillingModelForJobs :one
SELECT COALESCE(billing_model, 'postpaid') AS billing_model
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status = 'active'
ORDER BY created_at DESC
LIMIT 1;

-- name: PrepaidUsageSettlementExists :one
SELECT EXISTS (
    SELECT 1
    FROM purser.prepaid_usage_settlements
    WHERE report_id = sqlc.arg(report_id)
);

-- name: SumPrepaidUsageSettlements :one
SELECT COALESCE(SUM(amount_micro), 0)::bigint AS amount_micro
FROM purser.prepaid_usage_settlements
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND billing_period_start = sqlc.arg(billing_period_start)
  AND billing_period_end = sqlc.arg(billing_period_end);

-- name: InsertPrepaidUsageSettlement :execrows
INSERT INTO purser.prepaid_usage_settlements (
    report_id, tenant_id, billing_period_start, billing_period_end,
    amount_micro, cumulative_amount_micro, currency
) VALUES (
    sqlc.arg(report_id), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(billing_period_start), sqlc.arg(billing_period_end),
    sqlc.arg(amount_micro), sqlc.arg(cumulative_amount_micro), sqlc.arg(currency)
)
ON CONFLICT (report_id) DO NOTHING;

-- name: EnsurePrepaidBalance :exec
INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
VALUES (sqlc.arg(tenant_id)::text::uuid, 0, sqlc.arg(currency))
ON CONFLICT (tenant_id, currency) DO NOTHING;

-- name: LockPrepaidBalance :one
SELECT balance_cents, balance_remainder_micro
FROM purser.prepaid_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency)
FOR UPDATE;

-- name: LockPrepaidBalanceCents :one
SELECT balance_cents FROM purser.prepaid_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency)
FOR UPDATE;

-- name: InsertInvoiceCreditBalanceTransaction :exec
INSERT INTO purser.balance_transactions (
    tenant_id, amount_cents, balance_after_cents,
    transaction_type, description, reference_id, reference_type, created_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_cents), sqlc.arg(balance_after_cents),
    'credit', sqlc.arg(description), sqlc.narg(reference_id)::text::uuid,
    sqlc.arg(reference_type), NOW()
);

-- name: GetBalanceTransactionAmountByReference :one
SELECT amount_cents
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND reference_type = sqlc.arg(reference_type)
  AND reference_id = sqlc.arg(reference_id)::text::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdatePrepaidBalance :exec
UPDATE purser.prepaid_balances
SET balance_cents = sqlc.arg(balance_cents), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency);

-- name: SumAppliedInvoiceCredit :one
SELECT COALESCE(SUM(-amount_cents), 0)::bigint AS applied_cents
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND reference_type = 'invoice_credit'
  AND description = sqlc.arg(description)
  AND amount_cents < 0;

-- name: InsertUsageBalanceTransaction :execrows
INSERT INTO purser.balance_transactions (
    tenant_id, amount_cents, balance_after_cents,
    transaction_type, description, reference_id, reference_type, created_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_cents), sqlc.arg(balance_after_cents),
    'usage', sqlc.arg(description), sqlc.arg(reference_id)::text::uuid, 'usage_summary', NOW()
)
ON CONFLICT (tenant_id, reference_type, reference_id)
    WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO NOTHING;

-- name: UpdatePrepaidBalanceWithRemainder :exec
UPDATE purser.prepaid_balances
SET balance_cents = sqlc.arg(balance_cents),
    balance_remainder_micro = sqlc.arg(balance_remainder_micro),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency);

-- name: GetPrepaidBalanceForJobs :one
SELECT balance_cents
FROM purser.prepaid_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency);

-- name: SuspendActiveTenantSubscription :execrows
UPDATE purser.tenant_subscriptions
SET status = 'suspended', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status = 'active';

-- name: ListSubscriptionsDueForInvoice :many
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
       bt.tier_name,
       bt.display_name,
       bt.billing_period
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
WHERE ts.status = 'active'
  AND bt.is_active = TRUE
  AND (
      (ts.mollie_next_payment_date IS NOT NULL
          AND ts.mollie_next_payment_date <= sqlc.arg(now)::date)
      OR (ts.billing_period_end IS NOT NULL
          AND ts.billing_period_end <= sqlc.arg(now))
      OR (ts.billing_period_end IS NULL
          AND (ts.next_billing_date IS NULL
              OR ts.next_billing_date <= sqlc.arg(now)))
  );

-- name: CountFinalizedInvoicesForPeriod :one
SELECT COUNT(*)
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start = sqlc.arg(period_start)
  AND status NOT IN ('draft', 'manual_review');

-- name: GetDraftInvoiceIDForPeriod :one
SELECT id::text AS id
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start = sqlc.arg(period_start)
  AND status IN ('draft', 'manual_review')
LIMIT 1;

-- name: UpdateDraftInvoice :one
UPDATE purser.billing_invoices
SET amount = sqlc.arg(amount)::text::numeric,
    base_amount = sqlc.arg(base_amount)::text::numeric,
    metered_amount = sqlc.arg(metered_amount)::text::numeric,
    prepaid_credit_applied = sqlc.arg(prepaid_credit_applied)::text::numeric,
    currency = sqlc.arg(currency),
    status = sqlc.arg(status),
    due_date = sqlc.arg(due_date),
    usage_details = sqlc.arg(usage_details)::jsonb,
    period_start = sqlc.arg(period_start),
    period_end = sqlc.arg(period_end),
    gross_metered_amount = sqlc.arg(gross_metered_amount)::text::numeric,
    updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('draft', 'manual_review')
RETURNING id::text AS id;

-- name: UpsertInvoiceForPeriod :one
INSERT INTO purser.billing_invoices (
    id, tenant_id, amount, currency, status, due_date,
    base_amount, metered_amount, prepaid_credit_applied,
    usage_details, period_start, period_end, gross_metered_amount,
    created_at, updated_at
) VALUES (
    sqlc.arg(invoice_id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(amount)::text::numeric, sqlc.arg(currency), sqlc.arg(status), sqlc.arg(due_date),
    sqlc.arg(base_amount)::text::numeric, sqlc.arg(metered_amount)::text::numeric,
    sqlc.arg(prepaid_credit_applied)::text::numeric, sqlc.arg(usage_details)::jsonb,
    sqlc.arg(period_start), sqlc.arg(period_end),
    sqlc.arg(gross_metered_amount)::text::numeric, NOW(), NOW()
)
ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
DO UPDATE SET
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    status = EXCLUDED.status,
    due_date = EXCLUDED.due_date,
    base_amount = EXCLUDED.base_amount,
    metered_amount = EXCLUDED.metered_amount,
    prepaid_credit_applied = EXCLUDED.prepaid_credit_applied,
    usage_details = EXCLUDED.usage_details,
    period_end = EXCLUDED.period_end,
    gross_metered_amount = EXCLUDED.gross_metered_amount,
    updated_at = NOW()
WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
RETURNING id::text AS id;

-- name: MarkUsageAdjustmentsAppliedToInvoice :exec
UPDATE purser.usage_adjustments
SET applied_invoice_id = sqlc.arg(invoice_id)::text::uuid,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start < sqlc.arg(period_end)
  AND period_end > sqlc.arg(period_start)
  AND status = 'applied'
  AND value_kind = 'correction_delta'
  AND applied_invoice_id IS NULL;

-- name: AdvanceSubscriptionBillingPeriod :execrows
UPDATE purser.tenant_subscriptions
SET next_billing_date = sqlc.narg(next_billing_date)::timestamp,
    billing_period_start = sqlc.narg(billing_period_start)::timestamp,
    billing_period_end = sqlc.narg(billing_period_end)::timestamp,
    mollie_next_payment_date = CASE
        WHEN mollie_next_payment_date IS NOT NULL THEN sqlc.narg(billing_period_end)::date
        ELSE mollie_next_payment_date
    END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: CountActiveMeteringSources :one
SELECT COUNT(*)
FROM purser.metering_sources
WHERE required = TRUE
  AND active_from < sqlc.arg(period_end)::timestamptz
  AND (active_until IS NULL OR active_until > sqlc.arg(period_start)::timestamptz);

-- name: CountMissingMeteringWindows :one
SELECT COUNT(*)
FROM purser.metering_sources source
CROSS JOIN LATERAL generate_series(
    date_bin(
        INTERVAL '5 minutes',
        GREATEST(source.active_from, sqlc.arg(period_start)::timestamptz),
        TIMESTAMPTZ '1970-01-01'
    ) + CASE
        WHEN date_bin(
            INTERVAL '5 minutes',
            GREATEST(source.active_from, sqlc.arg(period_start)::timestamptz),
            TIMESTAMPTZ '1970-01-01'
        ) < GREATEST(source.active_from, sqlc.arg(period_start)::timestamptz)
        THEN INTERVAL '5 minutes'
        ELSE INTERVAL '0'
    END,
    LEAST(COALESCE(source.active_until, sqlc.arg(period_end)::timestamptz), sqlc.arg(period_end)::timestamptz)
        - INTERVAL '5 minutes',
    INTERVAL '5 minutes'
) AS expected(window_start)
LEFT JOIN purser.metering_windows mw
  ON mw.source_id = source.source_id
 AND mw.period_start = expected.window_start
 AND mw.period_end = expected.window_start + INTERVAL '5 minutes'
 AND mw.complete = TRUE
WHERE source.required = TRUE
  AND source.active_from < sqlc.arg(period_end)::timestamptz
  AND (source.active_until IS NULL OR source.active_until > sqlc.arg(period_start)::timestamptz)
  AND mw.source_id IS NULL;

-- name: CountOpenMeteringAnomalies :one
SELECT COUNT(*)
FROM purser.metering_anomalies
WHERE status = 'open'
  AND (tenant_id IS NULL OR tenant_id = sqlc.arg(tenant_id)::text::uuid)
  AND created_at < sqlc.arg(period_end);

-- name: ListDuePendingDowngradeTenantIDs :many
SELECT subscription.tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions subscription
WHERE subscription.status = 'active'
  AND subscription.pending_tier_id IS NOT NULL
  AND subscription.pending_effective_at <= sqlc.arg(now)
  AND EXISTS (
      SELECT 1
      FROM purser.billing_invoices invoice
      WHERE invoice.tenant_id = subscription.tenant_id
        AND invoice.period_end = subscription.pending_effective_at
        AND invoice.status NOT IN ('draft', 'manual_review')
  )
ORDER BY subscription.pending_effective_at ASC, subscription.tenant_id ASC;

-- name: GetActiveStripeCollectionDetails :one
SELECT stripe_customer_id, stripe_subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status = 'active'
  AND stripe_subscription_id IS NOT NULL;

-- name: GetLatestProviderPaymentAttempt :one
SELECT bpa.attempt_number, bpa.status
FROM purser.billing_payment_attempts bpa
JOIN purser.billing_payments bp ON bp.id = bpa.payment_id
WHERE bpa.provider = sqlc.arg(provider)
  AND bp.invoice_id = sqlc.arg(invoice_id)::text::uuid
ORDER BY bpa.attempt_number DESC
LIMIT 1;

-- name: UpsertPendingProviderBillingPayment :one
INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at
) VALUES (
    sqlc.arg(payment_id)::text::uuid, sqlc.arg(invoice_id)::text::uuid,
    'card', sqlc.arg(amount)::text::numeric, sqlc.arg(currency),
    sqlc.arg(tx_id), 'pending', NOW(), NOW()
)
ON CONFLICT (id) DO UPDATE
SET updated_at = purser.billing_payments.updated_at
RETURNING COALESCE(tx_id, '') AS tx_id, status;

-- name: UpsertProviderPaymentIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    provider_customer_id, status, currency, amount_cents, idempotency_key, attempt_count
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(provider), sqlc.arg(purpose),
    'invoice', sqlc.arg(invoice_id)::text::uuid, sqlc.arg(provider_customer_id),
    'pending', sqlc.arg(currency), sqlc.arg(amount_cents), sqlc.arg(idempotency_key), 1
)
ON CONFLICT (provider, idempotency_key) DO UPDATE
SET attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id, attempt_count;

-- name: LinkBillingPaymentIntent :exec
UPDATE purser.billing_payments
SET intent_id = sqlc.arg(intent_id)::text::uuid, updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid;

-- name: InsertProviderBillingPaymentAttempt :exec
INSERT INTO purser.billing_payment_attempts (
    payment_id, intent_id, attempt_number, idempotency_key, provider, status
) VALUES (
    sqlc.arg(payment_id)::text::uuid, sqlc.arg(intent_id)::text::uuid,
    sqlc.arg(attempt_number), sqlc.arg(idempotency_key), sqlc.arg(provider), 'pending'
)
ON CONFLICT (payment_id, attempt_number) DO NOTHING;

-- name: PrepareProviderBillingPaymentAttemptRetry :exec
UPDATE purser.billing_payment_attempts
SET status = 'pending', next_retry_at = NULL, updated_at = NOW()
WHERE payment_id = sqlc.arg(payment_id)::text::uuid
  AND attempt_number = sqlc.arg(attempt_number)
  AND status = 'provider_call_failed';

-- name: SetProviderPaymentIntentFailure :exec
UPDATE purser.payment_provider_intents
SET status = sqlc.arg(status), last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: SetProviderBillingPaymentAttemptFailure :exec
UPDATE purser.billing_payment_attempts
SET status = sqlc.arg(status),
    failure_code = sqlc.arg(failure_code),
    failure_message = sqlc.arg(failure_message),
    next_retry_at = sqlc.narg(next_retry_at),
    updated_at = NOW()
WHERE payment_id = sqlc.arg(payment_id)::text::uuid
  AND attempt_number = sqlc.arg(attempt_number);

-- name: MarkPendingBillingPaymentFailed :exec
UPDATE purser.billing_payments
SET status = 'failed', updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid
  AND status = 'pending';

-- name: AttachProviderPaymentIDToBillingPayment :exec
UPDATE purser.billing_payments
SET tx_id = sqlc.arg(provider_payment_id), updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid
  AND status = 'pending';

-- name: AttachProviderPaymentIDToIntent :exec
UPDATE purser.payment_provider_intents
SET provider_payment_id = sqlc.arg(provider_payment_id), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: AttachOpenProviderPaymentIDToIntent :exec
UPDATE purser.payment_provider_intents
SET provider_payment_id = sqlc.arg(provider_payment_id),
    status = 'provider_open',
    updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: AttachProviderPaymentIDToAttempt :exec
UPDATE purser.billing_payment_attempts
SET provider_payment_id = sqlc.arg(provider_payment_id), updated_at = NOW()
WHERE payment_id = sqlc.arg(payment_id)::text::uuid
  AND attempt_number = sqlc.arg(attempt_number);

-- name: SetProviderPaymentIntentStatus :exec
UPDATE purser.payment_provider_intents
SET status = sqlc.arg(status), updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: GetActiveMollieCollectionDetails :one
SELECT mc.mollie_customer_id,
       COALESCE((SELECT mm.mollie_mandate_id
        FROM purser.mollie_mandates mm
        WHERE mm.tenant_id = sqlc.arg(tenant_id)::text::uuid
          AND mm.status = 'valid'
        ORDER BY mm.created_at DESC
        LIMIT 1), '')::text AS mollie_mandate_id,
       COALESCE((SELECT mm.status
        FROM purser.mollie_mandates mm
        WHERE mm.tenant_id = sqlc.arg(tenant_id)::text::uuid
        ORDER BY mm.created_at DESC
        LIMIT 1), '')::text AS mandate_status
FROM purser.mollie_customers mc
JOIN purser.tenant_subscriptions ts ON ts.tenant_id = mc.tenant_id
WHERE mc.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ts.status = 'active'
  AND ts.mollie_subscription_id IS NOT NULL;

-- name: RevokeValidMollieMandates :exec
UPDATE purser.mollie_mandates
SET status = 'revoked', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status = 'valid';

-- name: GetPendingDowngrade :one
SELECT subscription.tier_id::text AS tier_id,
       COALESCE(subscription.pending_tier_id::text, '')::text AS pending_tier_id,
       subscription.pending_effective_at,
       tier.tier_level,
       tier.tier_name
FROM purser.tenant_subscriptions subscription
LEFT JOIN purser.billing_tiers tier ON tier.id = subscription.pending_tier_id
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ApplyPendingDowngradeTier :execrows
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(pending_tier_id)::text::uuid, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND pending_tier_id = sqlc.arg(pending_tier_id)::text::uuid;

-- name: ClearAppliedPendingDowngrade :exec
UPDATE purser.tenant_subscriptions
SET pending_tier_id = NULL,
    pending_effective_at = NULL,
    pending_reason = NULL,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tier_id = sqlc.arg(tier_id)::text::uuid
  AND pending_tier_id = sqlc.arg(tier_id)::text::uuid;

-- name: ListProviderPaymentAttemptsForRetry :many
SELECT attempt.provider,
       invoice.tenant_id::text AS tenant_id,
       payment.invoice_id::text AS invoice_id,
       payment.amount::text AS amount,
       payment.currency
FROM purser.billing_payment_attempts attempt
JOIN purser.billing_payments payment ON payment.id = attempt.payment_id
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE attempt.status = 'provider_call_failed'
  AND attempt.next_retry_at IS NOT NULL
  AND attempt.next_retry_at <= NOW()
  AND attempt.attempt_number < sqlc.arg(max_attempts)
  AND invoice.status IN ('pending', 'overdue')
  AND NOT EXISTS (
      SELECT 1
      FROM purser.billing_payment_attempts newer
      JOIN purser.billing_payments newer_payment ON newer_payment.id = newer.payment_id
      WHERE newer.provider = attempt.provider
        AND newer_payment.invoice_id = payment.invoice_id
        AND newer.attempt_number > attempt.attempt_number
  )
ORDER BY attempt.next_retry_at ASC
LIMIT 50;

-- name: MarkPendingInvoicesOverdue :exec
UPDATE purser.billing_invoices
SET status = 'overdue', updated_at = NOW()
WHERE status = 'pending' AND due_date < NOW();

-- name: StageOverdueInvoiceReminders :execrows
INSERT INTO purser.invoice_email_outbox (
    invoice_id, tenant_id, recipient, notification_type, reminder_stage
)
SELECT invoice.id,
       invoice.tenant_id,
       BTRIM(subscription.billing_email),
       'overdue_reminder',
       stage.reminder_stage
FROM purser.billing_invoices invoice
JOIN purser.tenant_subscriptions subscription
  ON subscription.tenant_id = invoice.tenant_id
CROSS JOIN LATERAL (
    SELECT MAX(candidate) AS reminder_stage
    FROM UNNEST(ARRAY[1, 7, 14, 30]) AS candidate
    WHERE candidate <= FLOOR(EXTRACT(EPOCH FROM (NOW() - invoice.due_date)) / 86400)
) stage
WHERE invoice.status = 'overdue'
  AND subscription.status = 'active'
  AND BTRIM(COALESCE(subscription.billing_email, '')) <> ''
  AND stage.reminder_stage IS NOT NULL
  AND invoice.amount > COALESCE((
      SELECT SUM(payment.amount - (COALESCE(payment.reversed_amount_cents, 0)::numeric / 100))
      FROM purser.billing_payments payment
      WHERE payment.invoice_id = invoice.id
        AND payment.status = 'confirmed'
        AND payment.currency = invoice.currency
  ), 0)
ON CONFLICT (invoice_id, notification_type, reminder_stage) DO NOTHING;

-- name: ExpireStaleCryptoWallets :execrows
UPDATE purser.crypto_wallets
SET status = 'expired', updated_at = NOW()
WHERE status IN ('pending', 'confirming')
  AND expires_at < NOW();

-- name: InsertUsageRecordQuarantine :exec
INSERT INTO purser.usage_records_quarantine (
    tenant_id, cluster_id, usage_type, usage_value, usage_details,
    period_start, period_end, granularity, value_kind,
    rejected_reason, source, raw_payload
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), sqlc.arg(usage_type),
    sqlc.arg(usage_value)::double precision, sqlc.arg(usage_details)::jsonb,
    sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(granularity),
    sqlc.arg(value_kind), sqlc.arg(rejected_reason), sqlc.arg(source),
    sqlc.arg(raw_payload)::jsonb
);

-- name: UpsertCanonicalUsageRecord :exec
INSERT INTO purser.usage_records (
    tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
    source_id, report_id, usage_value, usage_details,
    period_start, period_end, granularity, value_kind, created_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), sqlc.arg(usage_type),
    sqlc.arg(unit), COALESCE(sqlc.arg(dimensions)::jsonb, '{}'::jsonb),
    sqlc.arg(dimension_key), sqlc.arg(source_id), sqlc.arg(report_id),
    sqlc.arg(usage_value)::double precision, sqlc.arg(usage_details)::jsonb,
    sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(granularity),
    sqlc.arg(value_kind), NOW()
)
ON CONFLICT (
    tenant_id, cluster_id, source_id, usage_type, dimension_key, period_start, period_end
) DO NOTHING;

-- name: UpsertProviderUsageRecord :exec
INSERT INTO purser.provider_usage_records (
    usage_tenant_id, work_cluster_id,
    provider_tenant_id, provider_cluster_id,
    usage_type, unit, usage_value, dimensions, dimension_key,
    source_id, report_id, period_start, period_end,
    granularity, value_kind, source, usage_details
) VALUES (
    sqlc.arg(usage_tenant_id)::text::uuid, sqlc.arg(work_cluster_id),
    sqlc.arg(provider_tenant_id), sqlc.arg(provider_cluster_id),
    sqlc.arg(usage_type), sqlc.arg(unit), sqlc.arg(usage_value)::double precision,
    COALESCE(sqlc.arg(dimensions)::jsonb, '{}'::jsonb), sqlc.arg(dimension_key),
    sqlc.arg(source_id), sqlc.arg(report_id), sqlc.arg(period_start),
    sqlc.arg(period_end), 'minute_5', 'delta', sqlc.arg(source),
    sqlc.arg(usage_details)::jsonb
)
ON CONFLICT (
    usage_tenant_id, work_cluster_id,
    provider_tenant_id, provider_cluster_id,
    source_id, usage_type, dimension_key,
    period_start, period_end
) DO NOTHING;

-- name: UpsertLegacyProviderUsageRecord :exec
INSERT INTO purser.provider_usage_records (
    usage_tenant_id, work_cluster_id,
    provider_tenant_id, provider_cluster_id,
    usage_type, unit, usage_value, dimensions, dimension_key,
    source_id, report_id, period_start, period_end,
    granularity, value_kind, source, usage_details
) VALUES (
    sqlc.arg(usage_tenant_id)::text::uuid, sqlc.arg(work_cluster_id),
    sqlc.arg(provider_tenant_id), sqlc.arg(provider_cluster_id),
    sqlc.arg(usage_type), sqlc.arg(unit), sqlc.arg(usage_value)::double precision,
    COALESCE(sqlc.arg(dimensions)::jsonb, '{}'::jsonb),
    encode(digest(COALESCE(sqlc.arg(dimensions)::jsonb, '{}'::jsonb)::text, 'sha256'), 'hex'),
    'legacy', sqlc.arg(report_id), sqlc.arg(period_start),
    sqlc.arg(period_end), 'minute_5', 'delta', sqlc.arg(source),
    sqlc.arg(usage_details)::jsonb
)
ON CONFLICT (
    usage_tenant_id, work_cluster_id,
    provider_tenant_id, provider_cluster_id,
    source_id, usage_type, dimension_key,
    period_start, period_end
) DO UPDATE SET
    unit = EXCLUDED.unit,
    usage_value = EXCLUDED.usage_value,
    report_id = EXCLUDED.report_id,
    source = EXCLUDED.source,
    usage_details = EXCLUDED.usage_details,
    updated_at = NOW();

-- name: GetMeterUnitForAdjustment :one
SELECT unit
FROM purser.meter_definitions
WHERE meter = sqlc.arg(meter);

-- name: UpsertUsageAdjustment :exec
INSERT INTO purser.usage_adjustments (
    tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key, delta_value,
    period_start, period_end, value_kind, status,
    source_system, source_id, reason, details
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), sqlc.arg(usage_type),
    sqlc.arg(unit), COALESCE(sqlc.arg(dimensions)::jsonb, '{}'::jsonb),
    sqlc.arg(dimension_key), sqlc.arg(delta_value)::double precision, sqlc.arg(period_start),
    sqlc.arg(period_end), 'correction_delta', 'applied', sqlc.arg(source_system),
    sqlc.arg(source_id), sqlc.arg(reason), sqlc.arg(details)::jsonb
)
ON CONFLICT (source_system, source_id) DO NOTHING;

-- name: GetSubscriptionProviderIDs :one
SELECT stripe_subscription_id, mollie_subscription_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: UpsertInvoiceDraft :one
INSERT INTO purser.billing_invoices (
    id, tenant_id, amount, currency, status, due_date,
    base_amount, metered_amount, prepaid_credit_applied, usage_details,
    period_start, period_end, gross_metered_amount,
    created_at, updated_at
) VALUES (
    gen_random_uuid(), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(amount)::text::numeric, sqlc.arg(currency), 'draft', sqlc.arg(due_date),
    sqlc.arg(base_amount)::text::numeric, sqlc.arg(metered_amount)::text::numeric,
    sqlc.arg(prepaid_credit_applied)::text::numeric, sqlc.arg(usage_details)::jsonb,
    sqlc.arg(period_start), sqlc.arg(period_end),
    sqlc.arg(gross_metered_amount)::text::numeric, NOW(), NOW()
)
ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
DO UPDATE SET
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    status = 'draft',
    due_date = EXCLUDED.due_date,
    base_amount = EXCLUDED.base_amount,
    metered_amount = EXCLUDED.metered_amount,
    prepaid_credit_applied = EXCLUDED.prepaid_credit_applied,
    usage_details = EXCLUDED.usage_details,
    period_end = EXCLUDED.period_end,
    gross_metered_amount = EXCLUDED.gross_metered_amount,
    updated_at = NOW()
WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
RETURNING id::text AS id;

-- name: BackfillSubscriptionPeriodFromDraft :exec
UPDATE purser.tenant_subscriptions
SET billing_period_start = COALESCE(billing_period_start, sqlc.arg(period_start)),
    billing_period_end = COALESCE(billing_period_end, sqlc.arg(period_end)),
    next_billing_date = COALESCE(next_billing_date, sqlc.arg(period_end)),
    updated_at = CASE
        WHEN billing_period_start IS NULL
          OR billing_period_end IS NULL
          OR next_billing_date IS NULL
        THEN NOW()
        ELSE updated_at
    END
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status = 'active';
