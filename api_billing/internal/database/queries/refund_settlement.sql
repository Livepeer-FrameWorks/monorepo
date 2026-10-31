-- name: EnqueueRefundSettlement :exec
-- Provider acceptance is distinct from confirmed bank settlement.
INSERT INTO purser.provider_webhook_inbox (provider, event_key, headers, raw_payload, next_attempt_at)
VALUES ('refund_settlement', sqlc.arg(event_key)::text,
        jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text,
                           'provider', sqlc.arg(provider)::text,
                           'provider_payment_id', sqlc.arg(payment_id)::text,
                           'reference_id', sqlc.arg(reference_id)::text,
                           'reference_kind', sqlc.arg(reference_kind)::text,
                           'invoice_id', sqlc.arg(invoice_id)::text,
                           'currency', sqlc.arg(currency)::text,
                           'amount_cents', sqlc.arg(amount_cents)::bigint::text),
        sqlc.arg(payload)::bytea, NOW() + INTERVAL '1 minute')
ON CONFLICT (provider, event_key) DO NOTHING;

-- name: HasRefundSettlementOwner :one
-- Authenticated provider callbacks have no tenant context until their payment
-- reference is resolved. This platform-wide lookup only selects an existing owner.
SELECT EXISTS (
    SELECT 1 FROM purser.provider_webhook_inbox
    WHERE provider = 'refund_settlement'
      AND headers->>'provider' = sqlc.arg(provider)::text
      AND headers->>'provider_payment_id' = sqlc.arg(payment_id)::text
)::boolean AS owned;

-- name: RecordSettledCompensationRefund :exec
INSERT INTO purser.provider_payment_objects (
    provider, object_type, provider_object_id, tenant_id,
    local_reference_type, local_reference_id, intent_id, metadata
)
VALUES (sqlc.arg(provider)::text, sqlc.arg(object_type)::text, sqlc.arg(refund_id)::text,
        sqlc.arg(tenant_id)::text::uuid, sqlc.narg(reference_type)::text,
        sqlc.narg(reference_id)::text::uuid, sqlc.narg(intent_id)::text::uuid,
        sqlc.arg(metadata)::jsonb)
ON CONFLICT (provider, object_type, provider_object_id) DO NOTHING;

-- name: ApplyCompensatedStripeInvoiceCredit :exec
-- An unactivated purchase owes its initial fee back to the consumer, so its
-- operator credit is void while bank settlement is pending. Confirmed receipts
-- preserve the correction after processed queue retention. Root locking orders
-- this with accrual creation; cumulative targets make replay exact.
WITH confirmed AS (
    SELECT COALESCE(SUM((metadata->>'amount_cents')::bigint), 0)::numeric AS refunded
    FROM purser.provider_payment_objects
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND provider = 'stripe' AND object_type IN ('refund', 'dispute')
      AND metadata->>'invoice_id' = sqlc.arg(invoice_id)::text
      AND metadata->>'currency' = sqlc.arg(currency)::text
      AND metadata->>'reference_kind' = 'checkout'
), promised AS (
    SELECT COALESCE(SUM(CASE WHEN provider = 'refund_settlement'
                            THEN (headers->>'amount_cents')::bigint ELSE 0 END), 0)::numeric AS refunded
    FROM purser.provider_webhook_inbox
    WHERE provider = 'refund_settlement'
      AND headers->>'provider' = 'stripe'
      AND headers->>'tenant_id' = sqlc.arg(tenant_id)::text
      AND headers->>'reference_kind' = 'checkout'
      AND headers->>'invoice_id' = sqlc.arg(invoice_id)::text
      AND headers->>'currency' = sqlc.arg(currency)::text
), receipts AS (
    SELECT GREATEST(confirmed.refunded, promised.refunded) AS refunded FROM confirmed CROSS JOIN promised
), originals AS MATERIALIZED (
    SELECT original.* FROM purser.operator_credit_ledger original
    WHERE original.source_type = 'stripe_subscription' AND original.entry_type = 'accrual'
      AND original.stripe_invoice_id = sqlc.arg(invoice_id)::text
      AND original.currency = sqlc.arg(currency)::text AND original.gross_cents > 0
), prior AS MATERIALIZED (
    SELECT reversed.reverses_ledger_id,
           -SUM(reversed.gross_cents)::bigint AS gross,
           -SUM(reversed.platform_fee_cents)::bigint AS fee
    FROM purser.operator_credit_ledger reversed
    WHERE reversed.entry_type = 'clawback'
      AND reversed.reverses_ledger_id = ANY (ARRAY(SELECT originals.id FROM originals))
    GROUP BY reversed.reverses_ledger_id
), accruals AS MATERIALIZED (
    SELECT original.*,
           LEAST(receipts.refunded, original.gross_cents)::bigint AS target_gross,
           ROUND(LEAST(receipts.refunded, original.gross_cents)
                 * original.platform_fee_cents::numeric / original.gross_cents)::bigint AS target_fee,
           COALESCE(prior.gross, 0)::bigint AS prior_gross,
           COALESCE(prior.fee, 0)::bigint AS prior_fee
    FROM originals original CROSS JOIN receipts
    LEFT JOIN prior ON prior.reverses_ledger_id = original.id
    WHERE receipts.refunded > 0
), deltas AS (
    SELECT *, GREATEST(target_gross - prior_gross, 0) AS gross_delta,
              GREATEST(target_fee - prior_fee, 0) AS fee_delta
    FROM accruals
)
INSERT INTO purser.operator_credit_ledger (
    source_type, stripe_invoice_id, entry_type, reverses_ledger_id,
    cluster_owner_tenant_id, cluster_id, period_start, period_end, currency,
    gross_cents, platform_fee_cents, payable_cents, status, notes
)
SELECT 'stripe_subscription', stripe_invoice_id, 'clawback', id,
       cluster_owner_tenant_id, cluster_id, period_start, period_end, currency,
       -gross_delta, -fee_delta, -(gross_delta - fee_delta),
       CASE WHEN status = 'held' THEN 'held' ELSE 'accruing' END,
       jsonb_build_object('reason', 'checkout_compensation', 'consuming_tenant_id', sqlc.arg(tenant_id)::text)
FROM deltas WHERE gross_delta > 0;

-- name: WakeRefundSettlementOwner :exec
-- A provider can reverse a chargeback after an earlier bank check completed.
-- Authenticated provider-payment routing selects the existing scoped work.
UPDATE purser.provider_webhook_inbox
SET status = CASE WHEN status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE, next_attempt_at = NOW(), processed_at = NULL,
    attempts = CASE WHEN status = 'processed' THEN 0 ELSE attempts END,
    wait_attempts = CASE WHEN status = 'processed' THEN 0 ELSE wait_attempts END,
    unresolved_since = CASE WHEN status = 'processed' THEN NULL ELSE unresolved_since END,
    last_error = CASE WHEN status = 'processed' THEN NULL ELSE last_error END, updated_at = NOW()
WHERE provider = 'refund_settlement' AND status IN ('processed', 'processing')
  AND headers->>'provider' = sqlc.arg(provider)::text
  AND headers->>'provider_payment_id' = sqlc.arg(payment_id)::text;
