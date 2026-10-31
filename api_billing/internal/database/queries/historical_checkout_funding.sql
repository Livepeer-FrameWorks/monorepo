-- name: HasHistoricalMollieFundingActivation :one
WITH funding AS (
    SELECT f.id, f.provider_customer_id, f.created_at,
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
), candidates AS (
    SELECT native.id, funding.id AS funding_id,
        COALESCE(native.activated_at, native.succeeded_at) AS activated_at
    FROM purser.payment_provider_intents native
    JOIN funding ON funding.provider_customer_id = native.provider_customer_id
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.provider = 'mollie' AND native.purpose = 'mollie_subscription_create'
      AND native.provider_subscription_id = sqlc.arg(subscription_id)::text
      AND (native.activated_at IS NOT NULL OR native.succeeded_at IS NOT NULL)
      AND native.created_at >= funding.created_at
      AND (funding.next_created_at IS NULL OR native.created_at < funding.next_created_at)
      AND (native.funding_intent_id IS NULL OR native.funding_intent_id = funding.id)
)
SELECT EXISTS (SELECT 1 FROM candidates)::boolean AS activated;

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
-- A locally activated native checkout can prove its preceding first payment
-- within one customer's checkout sequence, after provider mandate verification.
WITH funding AS (
    SELECT f.id, f.provider_customer_id, f.created_at,
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
), candidates AS (
    SELECT native.id, funding.id AS funding_id,
        COALESCE(native.activated_at, native.succeeded_at) AS activated_at
    FROM purser.payment_provider_intents native
    JOIN funding ON funding.provider_customer_id = native.provider_customer_id
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND native.provider = 'mollie' AND native.purpose = 'mollie_subscription_create'
      AND native.provider_subscription_id = sqlc.arg(subscription_id)::text
      AND (native.activated_at IS NOT NULL OR native.succeeded_at IS NOT NULL)
      AND native.created_at >= funding.created_at
      AND (funding.next_created_at IS NULL OR native.created_at < funding.next_created_at)
      AND (native.funding_intent_id IS NULL OR native.funding_intent_id = funding.id)
), linked AS (
    UPDATE purser.payment_provider_intents native
    SET funding_intent_id = candidates.funding_id, updated_at = NOW()
    FROM candidates
    WHERE native.tenant_id = sqlc.arg(tenant_id)::text::uuid AND native.id = candidates.id
      AND (SELECT COUNT(*) FROM candidates) = 1
    RETURNING candidates.funding_id, candidates.activated_at
)
UPDATE purser.payment_provider_intents funding
SET activated_at = linked.activated_at, updated_at = NOW()
FROM linked
WHERE funding.tenant_id = sqlc.arg(tenant_id)::text::uuid AND funding.id = linked.funding_id;

-- name: RetainDeliveredHistoricalMollieCheckout :exec
UPDATE purser.payment_provider_intents
SET status = 'succeeded', activated_at = COALESCE(activated_at, NOW()),
    recovery_outcome = 'delivered', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid;
