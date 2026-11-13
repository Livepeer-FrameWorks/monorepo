-- name: EnqueueProviderWebhook :exec
INSERT INTO purser.provider_webhook_inbox
    (provider, event_key, headers, raw_payload)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, event_key) DO NOTHING;

-- name: ClaimProviderWebhooks :many
SELECT id, provider, headers, raw_payload, GREATEST(attempts,wait_attempts)::integer AS attempts, created_at, event_key
FROM purser.provider_webhook_inbox
WHERE next_attempt_at <= NOW()
  AND (status IN ('pending', 'failed')
       OR (status = 'processing'
           AND claimed_at < NOW() - (sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond')))
ORDER BY next_attempt_at, created_at
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: MarkProviderWebhookProcessing :exec
UPDATE purser.provider_webhook_inbox
SET status = 'processing', claimed_at = NOW(), wake_requested = FALSE,
    lease_token = sqlc.arg(lease_token)::text::uuid, updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CompleteProviderWebhook :execresult
UPDATE purser.provider_webhook_inbox
SET status = CASE WHEN wake_requested THEN 'pending' ELSE 'processed' END,
    processed_at = CASE WHEN NOT wake_requested THEN NOW() END,
    next_attempt_at = CASE WHEN wake_requested THEN NOW() ELSE next_attempt_at END, claimed_at = NULL,
    lease_token = NULL, last_error = NULL, last_error_code = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: FailProviderWebhook :execresult
UPDATE purser.provider_webhook_inbox
SET status = CASE WHEN sqlc.arg(blocked)::boolean THEN 'blocked'
                  WHEN wake_requested OR sqlc.arg(waiting)::boolean THEN 'pending' ELSE 'failed' END,
    attempts = attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 0 ELSE 1 END,
    wait_attempts = wait_attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 1 ELSE 0 END,
    unresolved_since = COALESCE(unresolved_since, NOW()),
    next_attempt_at = CASE WHEN wake_requested THEN NOW() ELSE NOW() + (sqlc.arg(backoff_milliseconds)::bigint * INTERVAL '1 millisecond') END,
    claimed_at = NULL, lease_token = NULL,
    last_error_code = NULLIF(sqlc.arg(error_code)::text, ''),
    last_error = sqlc.arg(last_error)::text || CASE WHEN provider_payment_id IS NOT NULL
        THEN ' (payment ' || left(provider_payment_id, 120) || ')' ELSE '' END, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: ProviderWebhookInboxRepeatedFailures :one
-- Service-wide recovery inventory; tenant filtering would hide stuck work.
SELECT COUNT(*)::bigint AS items FROM purser.billing_recovery_failures;

-- name: BindProviderWebhookPaymentOwner :execresult
UPDATE purser.provider_webhook_inbox inbox
SET tenant_id = payment.tenant_id, provider_payment_id = payment.provider_payment_id, updated_at = NOW()
FROM purser.provider_settlements payment
WHERE inbox.id = sqlc.arg(id) AND inbox.status = 'processing'
  AND inbox.lease_token::text = sqlc.arg(lease_token)::text
  AND inbox.provider = sqlc.arg(provider)::text AND payment.provider = inbox.provider
  AND payment.provider_payment_id = sqlc.arg(provider_payment_id)::text
  AND payment.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (inbox.tenant_id IS NULL OR inbox.tenant_id = payment.tenant_id)
  AND (inbox.provider_payment_id IS NULL OR inbox.provider_payment_id = payment.provider_payment_id);

-- name: WakeValuedProviderWebhooks :exec
UPDATE purser.provider_webhook_inbox
SET status='pending', next_attempt_at=NOW(), wake_requested=TRUE, updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND provider=sqlc.arg(provider)::text AND provider_payment_id=sqlc.arg(provider_payment_id)::text
  AND status='blocked' AND last_error_code='unsupported_currency';

-- name: ListCheckoutRecovery :many
-- Platform-operator inventory includes unbound callbacks; a supplied tenant narrows to its reservations.
-- Each disjoint branch reads an ordered index head before enrichment.
-- Unbound paid first payments require delivery review, never automatic compensation.
WITH selected AS (
    (SELECT inbox.* FROM purser.provider_webhook_inbox inbox
     WHERE inbox.status <> 'processed' AND sqlc.arg(tenant_id)::text = ''
     ORDER BY inbox.created_at, inbox.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
    UNION ALL
    (SELECT inbox.* FROM purser.provider_webhook_inbox inbox
     WHERE inbox.status <> 'processed' AND sqlc.arg(tenant_id)::text <> ''
       AND COALESCE(inbox.tenant_id::text, inbox.headers->>'tenant_id') = sqlc.arg(tenant_id)::text
     ORDER BY inbox.created_at, inbox.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
), page AS MATERIALIZED (
    SELECT selected.*,
           CASE WHEN selected.provider IN ('checkout_recovery', 'checkout_delivery_review', 'tier_upgrade')
                THEN convert_from(selected.raw_payload, 'UTF8')::jsonb END AS internal_payload
    FROM selected
    ORDER BY selected.created_at, selected.id
    LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint)
), inbox_rows AS (
SELECT i.id::text AS id, COALESCE(p.provider, i.headers->>'provider', i.provider)::text AS provider,
       i.event_key, i.status, i.attempts, i.last_error,
       CASE WHEN i.status <> 'blocked' THEN i.next_attempt_at END AS next_attempt_at,
       COALESCE(i.tenant_id::text, p.tenant_id::text, i.headers->>'tenant_id', i.internal_payload->>'tenant_id', '')::text AS tenant_id,
       COALESCE(p.id::text, upgrade.id::text, i.headers->>'reference_id', '')::text AS reservation_id,
       COALESCE(p.expires_at, p.created_at + INTERVAL '24 hours') AS expires_at,
       COALESCE(p.amount_cents, upgrade.base_cents, NULLIF(i.headers->>'amount_cents', '')::bigint, 0)::bigint AS amount_cents,
       COALESCE(p.currency, i.headers->>'currency', '')::text AS currency,
       COALESCE(p.status, upgrade.state, i.headers->>'reference_kind', '')::text AS reservation_status,
       i.created_at AS sort_at
FROM page i
LEFT JOIN purser.payment_provider_intents p
  ON p.id = CASE WHEN i.provider IN ('checkout_recovery', 'checkout_delivery_review')
                 THEN (i.internal_payload->>'checkout_reservation_id')::uuid END
 AND p.tenant_id = CASE WHEN i.provider IN ('checkout_recovery', 'checkout_delivery_review')
                        THEN (i.internal_payload->>'tenant_id')::uuid END
LEFT JOIN purser.subscription_tier_upgrades upgrade
  ON upgrade.id = CASE WHEN i.provider = 'tier_upgrade'
                       THEN (i.internal_payload->>'id')::uuid END
 AND upgrade.tenant_id = CASE WHEN i.provider = 'tier_upgrade'
                              THEN (i.headers->>'tenant_id')::uuid END
), fallback_selected AS (
    (SELECT subscription.* FROM purser.tenant_subscriptions subscription
     WHERE subscription.free_fallback_at IS NOT NULL AND sqlc.arg(tenant_id)::text = ''
     ORDER BY subscription.free_fallback_at, subscription.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
    UNION ALL
    (SELECT subscription.* FROM purser.tenant_subscriptions subscription
     WHERE subscription.free_fallback_at IS NOT NULL AND sqlc.arg(tenant_id)::text <> ''
       AND subscription.tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid
     ORDER BY subscription.free_fallback_at, subscription.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
), fallback_rows AS (
    SELECT subscription.id::text AS id, 'free_fallback'::text AS provider,
           ('free-fallback:' || subscription.tenant_id::text)::text AS event_key,
           CASE WHEN subscription.free_fallback_notice_abandoned_at IS NOT NULL THEN 'notice_abandoned'
                WHEN subscription.free_fallback_moved_at IS NOT NULL THEN 'notice'
                WHEN subscription.free_fallback_provider_stopping THEN 'provider_stopping'
                ELSE 'scheduled' END::text AS status,
           (subscription.free_fallback_failures + subscription.free_fallback_notice_failures)::integer AS attempts,
           NULLIF(concat_ws('; ', subscription.free_fallback_reason,
               subscription.free_fallback_error, subscription.free_fallback_notice_error,
               CASE WHEN subscription.free_fallback_notice_abandoned_at IS NOT NULL
                    THEN 'Repair billing_email, then run frameworks admin billing recovery notice --tenant-id ' || subscription.tenant_id::text || ' --reason <reason>' END), '') AS last_error,
           subscription.free_fallback_at::timestamptz AS next_attempt_at,
           subscription.tenant_id::text AS tenant_id, ''::text AS reservation_id,
           NULL::timestamptz AS expires_at, 0::bigint AS amount_cents, ''::text AS currency,
           'subscription'::text AS reservation_status, subscription.free_fallback_at AS sort_at
    FROM fallback_selected subscription
    ORDER BY subscription.free_fallback_at, subscription.id
    LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint)
), historical_selected AS (
    (SELECT intent.* FROM purser.payment_provider_intents intent
     WHERE sqlc.arg(tenant_id)::text = ''
       AND intent.provider = 'mollie' AND intent.purpose = 'mollie_first_payment'
       AND intent.status = 'succeeded' AND intent.amount_cents > 0
       AND intent.provider_payment_id IS NOT NULL AND intent.provider_mandate_id IS NULL
       AND intent.activated_at IS NULL AND intent.recovery_started_at IS NULL
       AND NOT EXISTS (
           SELECT 1 FROM purser.provider_webhook_inbox pending
           WHERE ((pending.provider = 'checkout_recovery' AND pending.event_key = 'checkout-expiry:' || intent.id::text)
               OR (pending.provider = 'checkout_delivery_review' AND pending.event_key = 'checkout-delivery-review:' || intent.id::text))
             AND pending.headers->>'tenant_id' = intent.tenant_id::text
             AND pending.status <> 'processed'
       )
     ORDER BY intent.created_at, intent.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
    UNION ALL
    (SELECT intent.* FROM purser.payment_provider_intents intent
     WHERE sqlc.arg(tenant_id)::text <> ''
       AND intent.tenant_id = NULLIF(sqlc.arg(tenant_id)::text, '')::uuid
       AND intent.provider = 'mollie' AND intent.purpose = 'mollie_first_payment'
       AND intent.status = 'succeeded' AND intent.amount_cents > 0
       AND intent.provider_payment_id IS NOT NULL AND intent.provider_mandate_id IS NULL
       AND intent.activated_at IS NULL AND intent.recovery_started_at IS NULL
       AND NOT EXISTS (
           SELECT 1 FROM purser.provider_webhook_inbox pending
           WHERE ((pending.provider = 'checkout_recovery' AND pending.event_key = 'checkout-expiry:' || intent.id::text)
               OR (pending.provider = 'checkout_delivery_review' AND pending.event_key = 'checkout-delivery-review:' || intent.id::text))
             AND pending.headers->>'tenant_id' = intent.tenant_id::text
             AND pending.status <> 'processed'
       )
     ORDER BY intent.created_at, intent.id
     LIMIT (sqlc.arg(row_limit)::bigint + sqlc.arg(row_offset)::bigint))
), historical_rows AS (
    SELECT intent.id::text AS id, intent.provider::text AS provider,
           ('historical-first-payment:' || intent.id::text)::text AS event_key,
           'delivery_review'::text AS status, 0::integer AS attempts,
           ('Payment ' || intent.provider_payment_id ||
            ': verify delivered service before confirming admin billing recovery checkout')::text AS last_error,
           NULL::timestamptz AS next_attempt_at,
           intent.tenant_id::text AS tenant_id, intent.id::text AS reservation_id,
           NULL::timestamptz AS expires_at, intent.amount_cents, intent.currency::text AS currency,
           intent.status::text AS reservation_status, intent.created_at AS sort_at
    FROM historical_selected intent
), settlement_selected AS (
    (SELECT settlement.* FROM purser.provider_settlements settlement
     WHERE settlement.status IN ('recorded','blocked') AND sqlc.arg(tenant_id)::text=''
     ORDER BY settlement.created_at,settlement.id
     LIMIT (sqlc.arg(row_limit)::bigint+sqlc.arg(row_offset)::bigint))
    UNION ALL
    (SELECT settlement.* FROM purser.provider_settlements settlement
     WHERE settlement.status IN ('recorded','blocked') AND sqlc.arg(tenant_id)::text<>''
       AND settlement.tenant_id=NULLIF(sqlc.arg(tenant_id)::text,'')::uuid
     ORDER BY settlement.created_at,settlement.id
     LIMIT (sqlc.arg(row_limit)::bigint+sqlc.arg(row_offset)::bigint))
), settlement_rows AS (
    SELECT ('settlement:'||settlement.id::text)::text AS id, settlement.provider::text AS provider,
           ('provider-settlement:'||settlement.provider_payment_id)::text AS event_key,
           settlement.status::text AS status,0::integer AS attempts,
           (COALESCE(settlement.last_error,'awaiting EUR reference valuation')||
            ' (payment '||left(settlement.provider_payment_id,120)||', native '||settlement.settlement_currency||')')::text AS last_error,
           CASE WHEN settlement.status='blocked' THEN NULL::timestamptz
                ELSE settlement.updated_at+INTERVAL '10 minutes' END AS next_attempt_at,
           settlement.tenant_id::text AS tenant_id,''::text AS reservation_id,
           NULL::timestamptz AS expires_at,settlement.charge_amount_cents AS amount_cents,
           settlement.charge_currency::text AS currency,'provider_settlement'::text AS reservation_status,
           settlement.created_at AS sort_at
    FROM settlement_selected settlement
), inventory AS (
    SELECT * FROM historical_rows
    UNION ALL
    SELECT * FROM inbox_rows
    UNION ALL
    SELECT * FROM fallback_rows
    UNION ALL
    SELECT * FROM settlement_rows
)
SELECT id::text AS id, provider, event_key, status, attempts,
       COALESCE(last_error, '')::text AS last_error, next_attempt_at,
       tenant_id, reservation_id, expires_at, amount_cents, currency, reservation_status
FROM inventory
ORDER BY sort_at, id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: RequestCheckoutRecovery :exec
INSERT INTO purser.provider_webhook_inbox (
    provider, event_key, headers, raw_payload, next_attempt_at
)
SELECT 'checkout_recovery', 'checkout-expiry:' || id::text,
       jsonb_build_object('recovery_reason', sqlc.arg(reason)::text, 'tenant_id', tenant_id::text),
       convert_to(jsonb_build_object(
           'checkout_reservation_id', id::text, 'tenant_id', tenant_id::text, 'force', true
       )::text, 'UTF8'), NOW()
FROM purser.payment_provider_intents
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(intent_id)::text::uuid
ON CONFLICT (provider, event_key) DO UPDATE SET
    attempts = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN 0 ELSE purser.provider_webhook_inbox.attempts END,
    wait_attempts = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN 0 ELSE purser.provider_webhook_inbox.wait_attempts END,
    unresolved_since = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN NULL ELSE purser.provider_webhook_inbox.unresolved_since END,
    last_error = CASE WHEN purser.provider_webhook_inbox.status = 'processed'
        THEN NULL ELSE purser.provider_webhook_inbox.last_error END,
    processed_at = NULL,
    headers = EXCLUDED.headers, raw_payload = EXCLUDED.raw_payload,
    next_attempt_at = LEAST(purser.provider_webhook_inbox.next_attempt_at, NOW()),
    status = CASE WHEN purser.provider_webhook_inbox.status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE, updated_at = NOW();

-- name: ProviderWebhookReceivedAt :one
SELECT created_at FROM purser.provider_webhook_inbox
WHERE provider = sqlc.arg(provider)::text
  AND event_key = sqlc.arg(event_key)::text
  AND ((provider = 'stripe' AND convert_from(raw_payload, 'UTF8')::jsonb#>>'{data,object,metadata,tenant_id}' = sqlc.arg(tenant_id)::text)
    OR (provider = 'mollie' AND convert_from(raw_payload, 'UTF8')::jsonb->>'tenant_id' = sqlc.arg(tenant_id)::text));

-- name: InsertCheckoutRecoveryAudit :one
INSERT INTO purser.checkout_recovery_audit (tenant_id, intent_id, actor_id, reason)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(intent_id)::text::uuid,
        sqlc.arg(actor_id)::text, sqlc.arg(reason)::text)
RETURNING id::text;

-- name: FinishCheckoutRecoveryAudit :exec
UPDATE purser.checkout_recovery_audit
SET outcome = sqlc.arg(outcome)::text, detail = sqlc.arg(detail)::text,
    completed_at = CASE WHEN sqlc.arg(outcome)::text IN ('requested', 'pending') THEN NULL ELSE NOW() END
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND id = sqlc.arg(id)::text::uuid
  AND outcome IN ('requested', 'pending');

-- name: ListCheckoutRecoveryAudit :many
SELECT id::text, intent_id::text, actor_id, reason, outcome, detail, created_at
FROM purser.checkout_recovery_audit
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY created_at DESC, id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: PendingCheckoutRecoveryAudits :many
SELECT id::text, actor_id, reason
FROM purser.checkout_recovery_audit
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND intent_id = sqlc.arg(intent_id)::text::uuid
  AND outcome IN ('requested', 'pending')
FOR UPDATE;

-- name: EnqueueSuspendedTenantStop :exec
INSERT INTO purser.provider_webhook_inbox AS inbox (
    provider, event_key, headers, raw_payload
)
VALUES ('tenant_suspension', 'tenant-suspension:' || sqlc.arg(tenant_id)::text,
        jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text),
        convert_to(jsonb_build_object('tenant_id', sqlc.arg(tenant_id)::text)::text, 'UTF8'))
ON CONFLICT (provider, event_key) DO UPDATE SET
    status = CASE WHEN inbox.status = 'processing' THEN 'processing' ELSE 'pending' END,
    wake_requested = TRUE, next_attempt_at = NOW(), processed_at = NULL,
    attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.attempts END,
    wait_attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.wait_attempts END,
    unresolved_since = CASE WHEN inbox.status = 'processed' THEN NULL ELSE inbox.unresolved_since END,
    updated_at = NOW();

-- name: RecordPolledSettlementConflict :one
-- A polled provider conflict is durable operator work before its scan cursor advances.
WITH inserted AS (
 INSERT INTO purser.provider_webhook_inbox
  (provider, tenant_id, provider_payment_id, event_key, headers, raw_payload,
   status, unresolved_since, last_error_code, last_error)
 SELECT payment.provider, payment.tenant_id, payment.provider_payment_id,
  sqlc.arg(event_key)::text, '{"Content-Type":"application/x-www-form-urlencoded"}'::jsonb,
  sqlc.arg(raw_payload)::bytea, 'blocked', NOW(), 'evidence_conflict',
  'provider settlement blocked: evidence_conflict'
 FROM purser.provider_settlements payment
 WHERE payment.tenant_id=sqlc.arg(tenant_id)::text::uuid
  AND payment.provider='mollie' AND payment.provider_payment_id=sqlc.arg(provider_payment_id)::text
 ON CONFLICT(provider,event_key) DO NOTHING
 RETURNING id
)
SELECT id FROM inserted
UNION ALL
SELECT id FROM purser.provider_webhook_inbox
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND provider='mollie'
 AND provider_payment_id=sqlc.arg(provider_payment_id)::text AND event_key=sqlc.arg(event_key)::text
LIMIT 1;

-- name: GetProviderWebhookRetryState :one
SELECT id::text AS id, provider, COALESCE(provider_payment_id,'') AS provider_payment_id,
 status, COALESCE(last_error_code,'') AS last_error_code, attempts, updated_at
FROM purser.provider_webhook_inbox
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(webhook_id)::text::uuid
FOR UPDATE;

-- name: FindProviderWebhookRetryAudit :one
SELECT id::text AS id FROM purser.provider_webhook_retry_audit
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND webhook_id=sqlc.arg(webhook_id)::text::uuid
 AND expected_state=sqlc.arg(expected_state)::text;

-- name: InsertProviderWebhookRetryAudit :one
INSERT INTO purser.provider_webhook_retry_audit
 (tenant_id,webhook_id,provider,provider_payment_id,previous_error_code,expected_state,actor_id,reason)
VALUES (sqlc.arg(tenant_id)::text::uuid,sqlc.arg(webhook_id)::text::uuid,sqlc.arg(provider),
 sqlc.arg(provider_payment_id),sqlc.arg(previous_error_code),sqlc.arg(expected_state),
 sqlc.arg(actor_id)::text::uuid,sqlc.arg(reason))
RETURNING id::text AS id;

-- name: RequestProviderWebhookRetry :execrows
UPDATE purser.provider_webhook_inbox
SET status='pending',next_attempt_at=NOW(),claimed_at=NULL,lease_token=NULL,
 wake_requested=FALSE,updated_at=NOW()
WHERE tenant_id=sqlc.arg(tenant_id)::text::uuid AND id=sqlc.arg(webhook_id)::text::uuid
 AND status='blocked' AND provider IN ('stripe','mollie');

-- name: ResolveStripeWebhookOwners :many
-- Provider-boundary lookup; tenant is established from retained local ownership,
-- never from callback headers or customer-supplied metadata.
SELECT DISTINCT COALESCE(owned.tenant_id::text,'')::text AS tenant_id FROM (
 (SELECT tenant_id FROM purser.provider_settlements
  WHERE provider='stripe' AND provider_payment_id=sqlc.arg(payment_id)::text LIMIT 2)
 UNION ALL
 (SELECT tenant_id FROM purser.provider_payment_objects
  WHERE provider='stripe' AND object_type=sqlc.arg(object_type)::text
   AND provider_object_id=sqlc.arg(object_id)::text AND tenant_id IS NOT NULL LIMIT 2)
 UNION ALL
 (SELECT DISTINCT tenant_id FROM purser.tenant_subscriptions
  WHERE stripe_customer_id=NULLIF(sqlc.arg(customer_id)::text,'') ORDER BY tenant_id LIMIT 2)
 UNION ALL
 (SELECT DISTINCT tenant_id FROM purser.cluster_subscriptions
  WHERE stripe_customer_id=NULLIF(sqlc.arg(customer_id)::text,'') ORDER BY tenant_id LIMIT 2)
 UNION ALL
 (SELECT DISTINCT tenant_id FROM purser.payment_provider_intents
  WHERE provider='stripe' AND provider_customer_id=NULLIF(sqlc.arg(customer_id)::text,'') ORDER BY tenant_id LIMIT 2)
) owned WHERE tenant_id IS NOT NULL LIMIT 2;

-- name: BindStripeWebhookResourceOwner :execresult
UPDATE purser.provider_webhook_inbox
SET tenant_id=sqlc.arg(tenant_id)::text::uuid,
 provider_payment_id=COALESCE(NULLIF(sqlc.arg(payment_id)::text,''),provider_payment_id), updated_at=NOW()
WHERE id=sqlc.arg(id)::uuid AND provider='stripe' AND status='processing'
 AND lease_token::text=sqlc.arg(lease_token)::text
 AND (tenant_id IS NULL OR tenant_id=sqlc.arg(tenant_id)::text::uuid)
 AND (sqlc.arg(payment_id)::text='' OR provider_payment_id IS NULL OR provider_payment_id=sqlc.arg(payment_id)::text);
