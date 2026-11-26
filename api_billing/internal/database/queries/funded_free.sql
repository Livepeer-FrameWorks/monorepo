-- name: GetFundedFreeCandidate :one
-- Read again under the subscription lock before activating funded usage.
SELECT subscription.id::text AS subscription_id
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
JOIN purser.prepaid_balances balance ON balance.tenant_id = subscription.tenant_id
  AND balance.currency = 'EUR' AND balance.balance_cents > 0
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tier.tier_name = 'free' AND tier.base_price = 0
  AND subscription.automatic_prepaid_usage
  AND subscription.billing_model = 'postpaid' AND subscription.status = 'active'
  AND subscription.pending_tier_id IS NULL
  AND NOT subscription.free_fallback_provider_stopping
  AND NULLIF(subscription.stripe_subscription_id, '') IS NULL
  AND NULLIF(subscription.mollie_subscription_id, '') IS NULL
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = subscription.tenant_id)
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id = subscription.id);

-- name: ListFundedFreeCandidates :many
-- Service-owned backstop for settled funding whose post-commit callback was lost.
-- Keyset paging keeps one blocked tenant from starving later funded accounts.
SELECT subscription.tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
JOIN purser.prepaid_balances balance ON balance.tenant_id = subscription.tenant_id
  AND balance.currency = 'EUR' AND balance.balance_cents > 0
WHERE subscription.tenant_id > sqlc.arg(after_tenant_id)::text::uuid
  AND tier.tier_name = 'free' AND tier.base_price = 0
  AND subscription.automatic_prepaid_usage
  AND subscription.billing_model = 'postpaid' AND subscription.status = 'active'
  AND subscription.pending_tier_id IS NULL
  AND NOT subscription.free_fallback_provider_stopping
  AND NULLIF(subscription.stripe_subscription_id, '') IS NULL
  AND NULLIF(subscription.mollie_subscription_id, '') IS NULL
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = subscription.tenant_id)
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id = subscription.id)
ORDER BY subscription.tenant_id LIMIT 100;

-- name: SetAutomaticPrepaidUsage :execrows
UPDATE purser.tenant_subscriptions
SET automatic_prepaid_usage = sqlc.arg(enabled)::boolean, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND automatic_prepaid_usage IS DISTINCT FROM sqlc.arg(enabled)::boolean;

-- name: RetainCurrentFreeFundingPreference :one
UPDATE purser.tenant_subscriptions AS subscription
SET automatic_prepaid_usage = FALSE, updated_at = NOW()
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND subscription.tier_id = sqlc.arg(tier_id)::text::uuid
  AND subscription.billing_model = 'postpaid'
  AND subscription.automatic_prepaid_usage
  AND EXISTS (SELECT 1 FROM purser.billing_tiers tier WHERE tier.id=subscription.tier_id AND tier.tier_name='free')
RETURNING subscription.id::text AS subscription_id, subscription.status;

-- name: GetExhaustedPrepaidCandidate :one
-- Grace ends at its deadline or exposure ceiling. Closing the paid phase
-- preserves debt; physical media limits are enforced per operation on Free.
SELECT subscription.id::text AS subscription_id
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
JOIN purser.prepaid_balances balance ON balance.tenant_id = subscription.tenant_id
  AND balance.currency = 'EUR' AND balance.balance_cents <= 0
WHERE subscription.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tier.is_default_prepaid AND tier.is_active AND tier.base_price = 0
  AND subscription.billing_model = 'prepaid' AND subscription.status = 'active'
  AND subscription.pending_tier_id IS NULL
  AND NOT subscription.free_fallback_provider_stopping
  AND subscription.free_fallback_at IS NULL
  AND NULLIF(subscription.stripe_subscription_id, '') IS NULL
  AND NULLIF(subscription.mollie_subscription_id, '') IS NULL
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_billing_closures closure WHERE closure.tenant_id = subscription.tenant_id)
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_operator_grants grant_row WHERE grant_row.subscription_id = subscription.id)
  AND NOT EXISTS (SELECT 1 FROM purser.subscription_pricing_overrides price WHERE price.subscription_id = subscription.id)
  AND NOT EXISTS (SELECT 1 FROM purser.cluster_subscriptions contract WHERE contract.tenant_id = subscription.tenant_id AND contract.status <> 'cancelled')
  AND (balance.grace_ends_at IS NULL OR balance.grace_ends_at <= NOW() OR balance.balance_cents <= -balance.grace_credit_cents);


-- name: ListExhaustedPrepaidCandidates :many
-- Keyset-paged repair for lost zero-crossing callbacks and eligibility changes.
SELECT subscription.tenant_id::text AS tenant_id
FROM purser.tenant_subscriptions subscription
JOIN purser.billing_tiers tier ON tier.id = subscription.tier_id
JOIN purser.prepaid_balances balance ON balance.tenant_id = subscription.tenant_id
  AND balance.currency = 'EUR' AND balance.balance_cents <= 0
WHERE subscription.tenant_id > sqlc.arg(after_tenant_id)::text::uuid
  AND tier.is_default_prepaid AND tier.is_active AND tier.base_price = 0
  AND subscription.billing_model = 'prepaid' AND subscription.status = 'active'
  AND subscription.pending_tier_id IS NULL
ORDER BY subscription.tenant_id LIMIT 100;

-- name: SumPriorFreeAllowanceUsage :one
-- Only finalized Free phases consume the remaining Free allowance. PAYG usage
-- retains its paid rates and never consumes or replenishes this allowance.
SELECT COALESCE(SUM(line.quantity), 0)::text AS used
FROM purser.billing_invoices invoice
JOIN purser.invoice_line_items line ON line.invoice_id = invoice.id AND line.tenant_id = invoice.tenant_id
WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.finalized_at IS NOT NULL AND invoice.document_kind = 'invoice'
  AND invoice.usage_details->'tier_info'->>'tier_name' = 'free'
  AND invoice.period_start >= sqlc.arg(period_start)::timestamptz
  AND invoice.period_end <= sqlc.arg(phase_start)::timestamptz
  AND line.meter = sqlc.arg(meter)::text;

-- name: GetAutomaticFreeTier :one
SELECT id::text AS id FROM purser.billing_tiers
WHERE tier_name = 'free' AND is_default_postpaid AND is_active AND base_price = 0
ORDER BY id LIMIT 1 FOR SHARE;

-- name: MoveExhaustedPrepaidToFree :execrows
-- Automatic fallback preserves the customer's funding preference.
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(tier_id)::text::uuid, billing_model = 'postpaid', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND billing_model = 'prepaid' AND status = 'active';

-- name: FreeTierChoiceHasFinancialHold :one
SELECT (
 EXISTS(SELECT 1 FROM purser.billing_collection_balances WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND balance_cents>0)
 OR EXISTS(SELECT 1 FROM purser.billing_invoices WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND document_kind='invoice' AND status IN ('pending','overdue','manual_review') AND amount>0)
 OR EXISTS(SELECT 1 FROM purser.cluster_subscriptions WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND status<>'cancelled')
 OR EXISTS(SELECT 1 FROM purser.usage_reservations WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND reserved_amount_micro>0 AND updated_at>=NOW()-INTERVAL '3 minutes')
 OR EXISTS(SELECT 1 FROM purser.subscription_pricing_overrides price JOIN purser.tenant_subscriptions subscription ON subscription.id=price.subscription_id WHERE subscription.tenant_id=sqlc.arg(tenant_id)::text::uuid)
)::boolean AS held;
