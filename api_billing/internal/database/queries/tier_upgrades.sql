-- name: GetPendingTierUpgrade :one
SELECT id::text, tier_id::text, state
FROM purser.subscription_tier_upgrades
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND state IN ('requested', 'provider_applied', 'pricing_applied');

-- name: GetTierUpgrade :one
SELECT id::text, tenant_id::text, old_tier_id::text, tier_id::text, actor_id,
    old_period_start, old_period_end, requested_at, provider, collection_provider, customer_id,
    provider_subscription_id, stripe_price_id, old_base_cents, base_cents,
    state, provider_receipt, recovery_requested_at, recovery_actor_id, recovery_reason,
    metering_waived_at, metering_waiver_actor_id
FROM purser.subscription_tier_upgrades
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(id)::text::uuid
FOR UPDATE;

-- name: InsertTierUpgrade :one
INSERT INTO purser.subscription_tier_upgrades (
    id, tenant_id, old_tier_id, tier_id, actor_id, old_period_start, old_period_end, requested_at,
    provider, collection_provider, customer_id, provider_subscription_id, stripe_price_id, old_base_cents, base_cents
)
SELECT sqlc.arg(id)::text::uuid, ts.tenant_id, ts.tier_id, target.id, sqlc.arg(actor_id)::text,
    sqlc.arg(period_start)::timestamptz, sqlc.arg(period_end)::timestamptz, clock_timestamp(),
    CASE WHEN NULLIF(ts.stripe_subscription_id, '') IS NOT NULL THEN 'stripe'
    WHEN NULLIF(ts.mollie_subscription_id, '') IS NOT NULL THEN 'mollie'
    WHEN ts.payment_method IN ('stripe', 'mollie') AND ts.presentment_currency <> 'EUR' THEN 'advance'
    ELSE 'invoice' END, COALESCE(ts.payment_method, ''),
    CASE WHEN ts.payment_method = 'mollie' THEN COALESCE(mc.mollie_customer_id, '') ELSE COALESCE(ts.stripe_customer_id, '') END,
    COALESCE(NULLIF(ts.stripe_subscription_id, ''), ts.mollie_subscription_id, ''),
    COALESCE(target.stripe_price_id_monthly, ''), ROUND(old.base_price*100)::bigint,
    ROUND(target.base_price*100)::bigint
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers old ON old.id = ts.tier_id
JOIN purser.billing_tiers target ON target.id = sqlc.arg(tier_id)::text::uuid
LEFT JOIN purser.mollie_customers mc ON mc.tenant_id = ts.tenant_id
WHERE ts.tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND COALESCE(target.currency, 'EUR') = 'EUR'
RETURNING id::text;

-- name: StageTierUpgrade :exec
UPDATE purser.tenant_subscriptions
SET pending_tier_id = sqlc.arg(tier_id)::text::uuid, pending_effective_at = sqlc.arg(requested_at)::timestamptz,
    pending_reason = 'tier_upgrade', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: EnqueueTierUpgrade :exec
INSERT INTO purser.provider_webhook_inbox AS inbox (provider, event_key, headers, raw_payload)
VALUES ('tier_upgrade', 'tier-upgrade:'||sqlc.arg(id)::text,
    jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text),
    convert_to(jsonb_build_object('id', sqlc.arg(id)::text, 'tenant_id', sqlc.arg(tenant_id)::text)::text, 'UTF8'))
ON CONFLICT (provider, event_key) DO UPDATE
SET status = 'pending', next_attempt_at = LEAST(inbox.next_attempt_at, NOW())
WHERE inbox.status IN ('pending', 'failed');

-- name: SetTierUpgradeProgress :exec
UPDATE purser.subscription_tier_upgrades
SET state = sqlc.arg(state)::text, provider_receipt = sqlc.arg(receipt)::jsonb, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(id)::text::uuid
    AND state NOT IN ('completed', 'cancelled');

-- name: ApplyTierUpgradePeriod :execrows
UPDATE purser.tenant_subscriptions
SET tier_id = sqlc.arg(tier_id)::text::uuid,
    billing_period_start = sqlc.arg(period_start)::timestamptz,
    billing_period_end = sqlc.arg(period_end)::timestamptz,
    next_billing_date = sqlc.arg(period_end)::timestamptz,
    stripe_current_period_end = CASE WHEN stripe_subscription_id IS NOT NULL THEN sqlc.arg(period_end)::timestamptz ELSE stripe_current_period_end END,
    mollie_next_payment_date = CASE WHEN mollie_subscription_id IS NOT NULL THEN sqlc.arg(period_end)::timestamptz::date ELSE mollie_next_payment_date END,
    pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND tier_id = sqlc.arg(old_tier_id)::text::uuid;

-- name: ClearCancelledTierUpgrade :exec
UPDATE purser.tenant_subscriptions
SET pending_tier_id = NULL, pending_effective_at = NULL, pending_reason = NULL, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND pending_reason = 'tier_upgrade';

-- name: ClearRejectedTierUpgrade :exec
UPDATE purser.tenant_subscriptions AS subscription
SET pending_tier_id=NULL,pending_effective_at=NULL,pending_reason=NULL,updated_at=NOW()
WHERE subscription.tenant_id=sqlc.arg(tenant_id)::text::uuid
 AND subscription.pending_reason='tier_upgrade'
 AND EXISTS (
  SELECT 1 FROM purser.subscription_tier_upgrades upgrade
  WHERE upgrade.tenant_id=subscription.tenant_id AND upgrade.id=sqlc.arg(id)::text::uuid
   AND upgrade.state='cancelled' AND upgrade.tier_id=subscription.pending_tier_id
   AND upgrade.requested_at=subscription.pending_effective_at
 );

-- name: GetTierUpgradeInvoice :one
SELECT i.id::text AS id, i.status, i.presentment_amount_cents,
    COALESCE(i.presentment_currency, i.currency)::text AS currency,
    EXISTS (SELECT 1 FROM purser.billing_payments p WHERE p.invoice_id = i.id) AS collection_started
FROM purser.billing_invoices i
WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND i.base_fee_period_start = sqlc.arg(period_start)::timestamptz;

-- name: GetStripeUpgradeBoundary :one
-- Subscription events preceding a paid price reset cannot restore its old tier or status.
SELECT (provider_receipt->>'PeriodStart')::timestamptz AS boundary, tier_id::text AS tier_id
FROM purser.subscription_tier_upgrades
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND provider = 'stripe' AND provider_subscription_id = sqlc.arg(subscription_id)::text
    AND state IN ('pricing_applied', 'completed')
ORDER BY (provider_receipt->>'PeriodStart')::timestamptz DESC
LIMIT 1;

-- name: ListUnfinishedTierUpgrades :many
SELECT id::text FROM purser.subscription_tier_upgrades
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
    AND state IN ('requested', 'provider_applied', 'pricing_applied')
ORDER BY requested_at, id;

-- name: RequestTierUpgradeRecovery :execrows
UPDATE purser.subscription_tier_upgrades
SET recovery_requested_at = COALESCE(recovery_requested_at, NOW()),
    recovery_actor_id = COALESCE(recovery_actor_id, sqlc.arg(actor_id)::text),
    recovery_reason = COALESCE(recovery_reason, sqlc.arg(reason)::text),
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(id)::text::uuid
    AND state NOT IN ('completed', 'cancelled') AND recovery_requested_at IS NULL;

-- name: WaiveTierUpgradeMetering :execrows
UPDATE purser.subscription_tier_upgrades
SET metering_waived_at = NOW(), metering_waiver_actor_id = sqlc.arg(actor_id)::text,
    metering_waiver_reason = sqlc.arg(reason)::text, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(id)::text::uuid
    AND state = 'provider_applied' AND metering_waived_at IS NULL;

-- name: HasPendingTierUpgrade :one
SELECT EXISTS (
    SELECT 1 FROM purser.subscription_tier_upgrades
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND state IN ('requested', 'provider_applied', 'pricing_applied')
)::boolean AS pending;
