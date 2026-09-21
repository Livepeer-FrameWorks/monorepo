-- name: GetTenantCollectionProfile :one
SELECT presentment_currency::text AS presentment_currency,
       status, billing_model, payment_method, stripe_customer_id,
       stripe_subscription_id, mollie_subscription_id,
       billing_period_start, billing_period_end, tier_id::text AS tier_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: SetInvoicePresentment :execrows
UPDATE purser.billing_invoices
SET presentment_amount_cents = sqlc.arg(presentment_amount_cents)::bigint,
    presentment_currency = sqlc.arg(presentment_currency)::text,
    presentment_units_per_eur = sqlc.arg(presentment_units_per_eur)::text::numeric,
    presentment_reference_date = sqlc.arg(presentment_reference_date)::date,
    finalized_at = sqlc.arg(finalized_at)::timestamptz,
    updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status NOT IN ('draft', 'manual_review');

-- name: GetInvoicePaymentFXBasis :one
-- The EUR amount an invoice was issued for, how it was presented, and what its
-- confirmed payments in the presentment currency already cover net of
-- reversals, as original and as EUR amounts.
SELECT ROUND(invoice.amount * 100)::bigint AS amount_cents,
       UPPER(invoice.currency)::text AS currency,
       invoice.presentment_amount_cents,
       COALESCE(invoice.presentment_currency, '')::text AS presentment_currency,
       COALESCE(invoice.presentment_units_per_eur::text, '')::text AS presentment_units_per_eur,
       invoice.presentment_reference_date,
       COALESCE((
           SELECT SUM(COALESCE(payment.original_amount_cents, 0) - COALESCE(payment.reversed_amount_cents, 0))
           FROM purser.billing_payments payment
           WHERE payment.invoice_id = invoice.id
             AND payment.status = 'confirmed'
             AND payment.original_currency = COALESCE(invoice.presentment_currency, UPPER(invoice.currency))
       ), 0)::bigint AS paid_original_cents,
       (COALESCE((
           SELECT SUM(COALESCE(payment.eur_amount_cents, 0))
           FROM purser.billing_payments payment
           WHERE payment.invoice_id = invoice.id
             AND payment.status = 'confirmed'
             AND payment.original_currency = COALESCE(invoice.presentment_currency, UPPER(invoice.currency))
       ), 0) - COALESCE((
           SELECT SUM(COALESCE(reversal.eur_amount_cents, 0))
           FROM purser.payment_reversals reversal
           JOIN purser.billing_payments payment ON payment.id = reversal.payment_id
           WHERE payment.invoice_id = invoice.id
             AND payment.status = 'confirmed'
             AND payment.original_currency = COALESCE(invoice.presentment_currency, UPPER(invoice.currency))
             AND reversal.status = 'succeeded'
       ), 0))::bigint AS paid_eur_cents
FROM purser.billing_invoices invoice
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: BaseFeeInvoiceExistsForPeriod :one
SELECT EXISTS (
    SELECT 1 FROM purser.billing_invoices
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND base_fee_period_start = sqlc.arg(period_start)::timestamptz
)::boolean AS present;

-- name: ListAdvanceBaseFeeCandidates :many
-- Tenants whose base fee Purser charges in advance: active postpaid tenants
-- without a provider subscription, presented outside EUR, with a card
-- provider on file, whose current period has no base-fee invoice yet.
SELECT ts.tenant_id::text AS tenant_id,
       ts.billing_period_start::timestamptz AS billing_period_start,
       ts.billing_period_end::timestamptz AS billing_period_end
FROM purser.tenant_subscriptions ts
WHERE ts.status = 'active'
  AND ts.billing_model = 'postpaid'
  AND ts.stripe_subscription_id IS NULL
  AND ts.mollie_subscription_id IS NULL
  AND ts.presentment_currency <> 'EUR'
  AND ts.payment_method IN ('stripe', 'mollie')
  AND ts.billing_period_start IS NOT NULL
  AND ts.billing_period_end IS NOT NULL
  AND ts.billing_period_start::timestamptz <= sqlc.arg(now)::timestamptz
  AND ts.billing_period_end::timestamptz > sqlc.arg(now)::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM purser.billing_invoices invoice
      WHERE invoice.tenant_id = ts.tenant_id
        AND invoice.base_fee_period_start = ts.billing_period_start::timestamptz
  )
ORDER BY ts.billing_period_start
LIMIT 200;

-- name: InsertBaseFeeInvoice :one
-- amount is what the invoice collects: the base fee less any credit for the
-- unused share of the previous period's base fee, never below zero.
INSERT INTO purser.billing_invoices (
    tenant_id, status, currency, amount, due_date,
    base_amount, metered_amount, gross_metered_amount, prepaid_credit_applied,
    usage_details, base_fee_period_start, base_fee_period_end,
    presentment_amount_cents, presentment_currency, presentment_units_per_eur,
    presentment_reference_date, finalized_at, created_at, updated_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(status)::text, 'EUR', sqlc.arg(amount)::text::numeric,
    sqlc.arg(due_date)::timestamptz,
    sqlc.arg(base_amount)::text::numeric, 0, 0, 0,
    sqlc.arg(usage_details)::jsonb, sqlc.arg(period_start)::timestamptz, sqlc.arg(period_end)::timestamptz,
    sqlc.arg(presentment_amount_cents)::bigint, sqlc.arg(presentment_currency)::text,
    sqlc.arg(presentment_units_per_eur)::text::numeric, sqlc.arg(presentment_reference_date)::date,
    sqlc.arg(finalized_at)::timestamptz, NOW(), NOW()
)
ON CONFLICT (tenant_id, base_fee_period_start) WHERE base_fee_period_start IS NOT NULL
DO NOTHING
RETURNING id::text AS id;

-- name: FindOverlappedBaseFeeInvoice :one
-- The base-fee invoice of the period a tier change cut short: it started
-- before period_start and would have run past it.
SELECT id::text AS id
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND base_fee_period_start < sqlc.arg(period_start)::timestamptz
  AND base_fee_period_end > sqlc.arg(period_start)::timestamptz
ORDER BY base_fee_period_start DESC
LIMIT 1;

-- name: LockOverlappedBaseFeeInvoice :one
-- Locks the base-fee invoice FindOverlappedBaseFeeInvoice found.
SELECT invoice.id::text AS id, invoice.status, invoice.base_amount::text AS base_amount,
       invoice.base_fee_period_start::timestamptz AS base_fee_period_start,
       invoice.base_fee_period_end::timestamptz AS base_fee_period_end,
       (SELECT COUNT(*) FROM purser.billing_payments payment
        WHERE payment.invoice_id = invoice.id AND payment.status = 'pending')::bigint AS pending_payments
FROM purser.billing_invoices invoice
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE OF invoice;

-- name: ReduceUnpaidBaseFeeInvoice :execrows
-- Lowers a payable base-fee invoice to the share of its period that was used,
-- presented at the rate it was issued at.
UPDATE purser.billing_invoices
SET amount = sqlc.arg(amount)::text::numeric,
    presentment_amount_cents = sqlc.arg(presentment_amount_cents)::bigint,
    usage_details = usage_details || sqlc.arg(details)::jsonb,
    updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('pending', 'overdue');

-- name: GetSubscriptionForInvoice :one
-- The columns ListSubscriptionsDueForInvoice returns, for one tenant whose
-- period is closed early.
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
           SELECT 1 FROM purser.mollie_customers mc WHERE mc.tenant_id = ts.tenant_id
       )::boolean AS has_mollie_customer,
       bt.tier_name,
       bt.display_name,
       bt.billing_period
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND ts.status = 'active';

-- name: GetFinalizedInvoicePeriodEnd :one
SELECT period_end::timestamptz AS period_end
FROM purser.billing_invoices
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND period_start = sqlc.arg(period_start)
  AND base_fee_period_start IS NULL
  AND status NOT IN ('draft', 'manual_review')
LIMIT 1;

-- name: ActivateAdvanceBilledSubscription :execrows
-- Applies the tier a payment-method setup was started for and starts the first
-- period at activation. The base fee for that period is then charged in
-- advance on a base-fee invoice. A replayed activation for the tier and
-- provider the tenant already runs on keeps the current period.
UPDATE purser.tenant_subscriptions
SET status = 'active',
    billing_model = 'postpaid',
    payment_method = sqlc.arg(payment_method)::text,
    stripe_customer_id = COALESCE(NULLIF(sqlc.arg(stripe_customer_id)::text, ''), stripe_customer_id),
    tier_id = COALESCE(
        NULLIF(sqlc.arg(tier_id)::text, '')::uuid,
        CASE WHEN pending_reason = 'stripe_checkout' THEN pending_tier_id END,
        tier_id
    ),
    pending_tier_id = CASE WHEN pending_reason = 'stripe_checkout' THEN NULL ELSE pending_tier_id END,
    pending_effective_at = CASE WHEN pending_reason = 'stripe_checkout' THEN NULL ELSE pending_effective_at END,
    pending_intent_id = CASE WHEN pending_reason = 'stripe_checkout' THEN NULL ELSE pending_intent_id END,
    pending_reason = CASE WHEN pending_reason = 'stripe_checkout' THEN NULL ELSE pending_reason END,
    billing_period_start = CASE WHEN current_period.kept THEN billing_period_start ELSE sqlc.arg(period_start)::timestamp END,
    billing_period_end = CASE WHEN current_period.kept THEN billing_period_end ELSE sqlc.arg(period_end)::timestamp END,
    next_billing_date = CASE WHEN current_period.kept THEN next_billing_date ELSE sqlc.arg(period_end)::timestamp END,
    updated_at = NOW()
FROM (
    SELECT (ts.status = 'active'
            AND ts.payment_method = sqlc.arg(payment_method)::text
            AND ts.billing_period_start IS NOT NULL
            AND ts.billing_period_end > NOW()
            AND ts.tier_id = COALESCE(
                NULLIF(sqlc.arg(tier_id)::text, '')::uuid,
                CASE WHEN ts.pending_reason = 'stripe_checkout' THEN ts.pending_tier_id END,
                ts.tier_id
            ))::boolean AS kept
    FROM purser.tenant_subscriptions ts
    WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
) AS current_period
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND stripe_subscription_id IS NULL
  AND mollie_subscription_id IS NULL;

-- name: InsertPendingProviderSettlement :exec
INSERT INTO purser.provider_settlements (
    tenant_id, provider, provider_payment_id, pending_topup_id, payment_id,
    charge_amount_cents, charge_currency
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(provider), sqlc.arg(provider_payment_id),
    sqlc.narg(pending_topup_id)::text::uuid, sqlc.narg(payment_id)::text::uuid,
    sqlc.arg(charge_amount_cents)::bigint, sqlc.arg(charge_currency)::text
)
ON CONFLICT (provider, provider_payment_id) DO NOTHING;

-- name: SettleProviderSettlement :execrows
UPDATE purser.provider_settlements
SET status = 'settled',
    provider_balance_transaction_id = sqlc.arg(balance_transaction_id),
    settled_amount_cents = sqlc.arg(settled_amount_cents)::bigint,
    fee_cents = sqlc.arg(fee_cents)::bigint,
    net_cents = sqlc.arg(net_cents)::bigint,
    settlement_currency = sqlc.arg(settlement_currency)::text,
    exchange_rate = sqlc.narg(exchange_rate)::text::numeric,
    settled_at = sqlc.arg(settled_at)::timestamptz,
    updated_at = NOW()
WHERE provider = sqlc.arg(provider)
  AND provider_payment_id = sqlc.arg(provider_payment_id)
  AND status = 'pending';

-- name: GetProviderSettlement :one
SELECT tenant_id::text AS tenant_id, status, charge_amount_cents, charge_currency::text AS charge_currency,
       COALESCE(provider_balance_transaction_id, '')::text AS provider_balance_transaction_id,
       settled_amount_cents, fee_cents, net_cents, COALESCE(exchange_rate::text, '')::text AS exchange_rate
FROM purser.provider_settlements
WHERE provider = sqlc.arg(provider) AND provider_payment_id = sqlc.arg(provider_payment_id);

-- name: ListPendingProviderSettlements :many
SELECT provider_payment_id, tenant_id::text AS tenant_id,
       charge_amount_cents, charge_currency::text AS charge_currency, created_at
FROM purser.provider_settlements
WHERE provider = sqlc.arg(provider)
  AND status = 'pending'
  AND updated_at <= sqlc.arg(updated_before)::timestamptz
ORDER BY updated_at
LIMIT 100;

-- name: TouchPendingProviderSettlement :exec
UPDATE purser.provider_settlements
SET updated_at = NOW()
WHERE provider = sqlc.arg(provider)
  AND provider_payment_id = sqlc.arg(provider_payment_id)
  AND status = 'pending';

-- name: RewindMollieBalanceCursorForPendingSettlement :execrows
-- Moves every Mollie balance cursor back to a payment's creation time while
-- that payment's settlement is still pending, so the next read reaches the
-- payment's balance transaction even though its local row was written after
-- the cursor passed it.
UPDATE purser.mollie_balance_cursors
SET last_transaction_id = NULL,
    last_transaction_created_at = sqlc.arg(payment_created_at)::timestamptz,
    updated_at = NOW()
WHERE last_transaction_created_at > sqlc.arg(payment_created_at)::timestamptz
  AND EXISTS (
      SELECT 1 FROM purser.provider_settlements settlement
      WHERE settlement.tenant_id = sqlc.arg(tenant_id)::text::uuid
        AND settlement.provider = 'mollie'
        AND settlement.provider_payment_id = sqlc.arg(provider_payment_id)
        AND settlement.status = 'pending'
  );

-- name: GetMollieBalanceCursor :one
SELECT COALESCE(last_transaction_id, '')::text AS last_transaction_id, last_transaction_created_at
FROM purser.mollie_balance_cursors
WHERE balance_id = sqlc.arg(balance_id);

-- name: EnsureMollieBalanceCursor :exec
INSERT INTO purser.mollie_balance_cursors (balance_id)
VALUES (sqlc.arg(balance_id))
ON CONFLICT (balance_id) DO NOTHING;

-- name: UpsertMollieBalanceCursor :exec
INSERT INTO purser.mollie_balance_cursors (balance_id, last_transaction_id, last_transaction_created_at, updated_at)
VALUES (sqlc.arg(balance_id), sqlc.arg(last_transaction_id), sqlc.arg(last_transaction_created_at)::timestamptz, NOW())
ON CONFLICT (balance_id) DO UPDATE
SET last_transaction_id = EXCLUDED.last_transaction_id,
    last_transaction_created_at = EXCLUDED.last_transaction_created_at,
    updated_at = NOW()
WHERE purser.mollie_balance_cursors.last_transaction_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(expected_id)::text, '')
  AND purser.mollie_balance_cursors.last_transaction_created_at IS NOT DISTINCT FROM sqlc.narg(expected_created_at)::timestamptz;

-- name: ActivatePurserInvoicedClusterSubscription :one
-- A monthly cluster fee billed on Purser invoices has no Stripe subscription.
-- activated_at starts the time the fee is billed for; reactivating a cancelled
-- subscription archives its closed interval before starting another one.
INSERT INTO purser.cluster_subscriptions (tenant_id, cluster_id, status, activated_at)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(cluster_id), 'active', NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE
SET status = 'active', cancelled_at = NULL,
    activated_at = CASE WHEN purser.cluster_subscriptions.status = 'active'
                        THEN COALESCE(purser.cluster_subscriptions.activated_at, purser.cluster_subscriptions.created_at, NOW())
                        ELSE NOW() END,
    updated_at = NOW()
WHERE purser.cluster_subscriptions.stripe_subscription_id IS NULL
RETURNING id::text AS id;

-- name: CancelPurserInvoicedClusterSubscription :execrows
UPDATE purser.cluster_subscriptions
SET status = 'cancelled', cancelled_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND cluster_id = sqlc.arg(cluster_id)
  AND stripe_subscription_id IS NULL
  AND status = 'active';

-- name: ListPurserInvoicedClusterSubscriptionsForPeriod :many
-- Monthly clusters billed on Purser invoices that were active at any point of
-- the period, with the time they were active from and their cancellation.
SELECT cluster_id, status,
       COALESCE(activated_at, created_at, NOW())::timestamptz AS active_from,
       cancelled_at
FROM purser.cluster_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND stripe_subscription_id IS NULL
  AND COALESCE(activated_at, created_at, NOW()) < sqlc.arg(period_end)::timestamptz
  AND (status = 'active' OR (status = 'cancelled' AND cancelled_at > sqlc.arg(period_start)::timestamptz))
UNION ALL
SELECT cluster_id, 'cancelled'::varchar(50) AS status, active_from, active_until AS cancelled_at
FROM purser.cluster_subscription_active_periods
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND active_from < sqlc.arg(period_end)::timestamptz
  AND active_until > sqlc.arg(period_start)::timestamptz
ORDER BY cluster_id;
