-- name: GetHistoricalMollieFundingActivation :one
-- One first-payment window may fund one locally delivered native subscription.
-- Native provider_open receipts alone are not evidence of delivery.
WITH funding AS (
    SELECT f.id, f.provider_customer_id, f.created_at, f.succeeded_at,
        (SELECT MIN(next.created_at) FROM purser.payment_provider_intents next
         WHERE next.tenant_id = f.tenant_id AND next.provider = 'mollie'
           AND next.purpose = 'mollie_first_payment'
           AND next.provider_customer_id = f.provider_customer_id
           AND next.created_at > f.created_at) AS next_created_at
    FROM purser.payment_provider_intents f
    WHERE f.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND f.id = sqlc.arg(intent_id)::text::uuid
      AND f.provider = 'mollie' AND f.purpose = 'mollie_first_payment'
      AND f.provider_mandate_id IS NULL AND f.activated_at IS NULL
      AND f.succeeded_at IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM purser.payment_provider_intents duplicate
          WHERE duplicate.tenant_id = f.tenant_id AND duplicate.provider = 'mollie'
            AND duplicate.purpose = 'mollie_first_payment'
            AND duplicate.provider_customer_id = f.provider_customer_id
            AND duplicate.created_at = f.created_at AND duplicate.id <> f.id)
), candidates AS (
    SELECT native.id::text AS native_id, native.provider_subscription_id,
        COALESCE(native.activated_at, evidence.resolved_at, native.created_at)::timestamp AS activated_at
    FROM purser.payment_provider_intents native
    JOIN funding ON funding.provider_customer_id = native.provider_customer_id
    LEFT JOIN purser.tenant_subscriptions subscription
      ON subscription.tenant_id = native.tenant_id
     AND subscription.mollie_subscription_id = native.provider_subscription_id
    LEFT JOIN LATERAL (
        SELECT observation.resolved_at
        FROM purser.mollie_payment_observations observation
        JOIN purser.billing_invoices invoice ON invoice.id = observation.invoice_id
          AND invoice.tenant_id = observation.tenant_id
        WHERE observation.tenant_id = native.tenant_id
          AND observation.mollie_subscription_id = native.provider_subscription_id
          AND observation.resolution = 'attached' AND observation.resolved_at IS NOT NULL
          AND invoice.status NOT IN ('draft', 'manual_review')
        LIMIT 1
    ) evidence ON true
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.provider = 'mollie' AND native.purpose = 'mollie_subscription_create'
      AND (native.activated_at IS NOT NULL OR subscription.tenant_id IS NOT NULL
           OR evidence.resolved_at IS NOT NULL)
      AND native.created_at >= funding.created_at
      AND (funding.next_created_at IS NULL OR native.created_at < funding.next_created_at)
      AND (native.funding_intent_id IS NULL OR native.funding_intent_id = funding.id)
    UNION ALL
    SELECT ''::text AS native_id, subscription.mollie_subscription_id,
        funding.succeeded_at::timestamp AS activated_at
    FROM funding
    JOIN purser.tenant_subscriptions subscription
      ON subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
    WHERE NULLIF(subscription.mollie_subscription_id, '') IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM purser.payment_provider_intents native
          WHERE native.tenant_id = subscription.tenant_id AND native.provider = 'mollie'
            AND native.purpose = 'mollie_subscription_create'
            AND native.provider_subscription_id = subscription.mollie_subscription_id)
      AND (SELECT COUNT(*) FROM purser.payment_provider_intents receipt
           WHERE receipt.tenant_id = subscription.tenant_id AND receipt.provider = 'mollie'
             AND receipt.purpose = 'mollie_first_payment'
             AND receipt.provider_customer_id = funding.provider_customer_id
             AND receipt.succeeded_at IS NOT NULL) = 1
)
SELECT native_id, activated_at FROM candidates
WHERE provider_subscription_id = sqlc.arg(subscription_id)::text
  AND (SELECT COUNT(*) FROM candidates) = 1;

-- name: HasMollieCheckoutRefundObligation :one
SELECT (
    EXISTS (
        SELECT 1 FROM purser.provider_webhook_inbox
        WHERE provider = 'refund_settlement'
          AND headers->>'tenant_id' = sqlc.arg(tenant_id)::text
          AND headers->>'provider' = 'mollie'
          AND headers->>'provider_payment_id' = sqlc.arg(payment_id)::text
    ) OR EXISTS (
        SELECT 1 FROM purser.provider_payment_objects
        WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
          AND provider = 'mollie' AND object_type IN ('refund', 'dispute')
          AND metadata->>'payment_id' = sqlc.arg(payment_id)::text
    )
)::boolean AS obligated;

-- name: BackfillHistoricalMollieCheckoutFunding :exec
-- The caller rechecks the canonical delivery proof under the subscription lock.
WITH linked AS (
    UPDATE purser.payment_provider_intents native
    SET funding_intent_id = sqlc.arg(intent_id)::text::uuid, updated_at = NOW()
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.id = sqlc.arg(native_id)::text::uuid
      AND (native.funding_intent_id IS NULL OR native.funding_intent_id = sqlc.arg(intent_id)::text::uuid)
    RETURNING funding_intent_id
)
UPDATE purser.payment_provider_intents funding
SET activated_at = sqlc.arg(activated_at)::timestamp, updated_at = NOW()
FROM linked
WHERE funding.tenant_id = sqlc.arg(tenant_id)::text::uuid AND funding.id = linked.funding_intent_id;

-- name: MarkHistoricalMollieDeliveryAmbiguous :exec
UPDATE purser.payment_provider_intents
SET recovery_outcome = 'ambiguous_delivery', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
  AND provider = 'mollie' AND purpose = 'mollie_first_payment'
  AND succeeded_at IS NOT NULL AND activated_at IS NULL AND provider_mandate_id IS NULL;

-- name: RetainDeliveredHistoricalMollieCheckout :execrows
UPDATE purser.payment_provider_intents
SET status = 'succeeded', activated_at = NOW(), recovery_outcome = 'delivered', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
  AND recovery_outcome = 'ambiguous_delivery'
  AND provider = 'mollie' AND purpose = 'mollie_first_payment'
  AND succeeded_at IS NOT NULL AND activated_at IS NULL AND provider_mandate_id IS NULL;
