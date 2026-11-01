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
    lease_token = NULL, last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: FailProviderWebhook :execresult
UPDATE purser.provider_webhook_inbox
SET status = CASE WHEN wake_requested OR sqlc.arg(waiting)::boolean THEN 'pending' ELSE 'failed' END,
    attempts = attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 0 ELSE 1 END,
    wait_attempts = wait_attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 1 ELSE 0 END,
    unresolved_since = COALESCE(unresolved_since, NOW()),
    next_attempt_at = CASE WHEN wake_requested THEN NOW() ELSE NOW() + (sqlc.arg(backoff_milliseconds)::bigint * INTERVAL '1 millisecond') END,
    claimed_at = NULL, lease_token = NULL,
    last_error = sqlc.arg(last_error)::text, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: ProviderWebhookInboxRepeatedFailures :one
-- Service-wide recovery inventory; tenant filtering would hide stuck work.
SELECT COUNT(*)::bigint AS items
FROM (
    SELECT id FROM purser.provider_webhook_inbox
    WHERE status IN ('pending', 'failed', 'processing') AND attempts > 1
    UNION
    SELECT id FROM purser.provider_webhook_inbox
    WHERE status IN ('pending', 'failed', 'processing')
      AND unresolved_since < NOW() - INTERVAL '14 days'
) unresolved;

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
       AND inbox.headers->>'tenant_id' = sqlc.arg(tenant_id)::text
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
       i.event_key, i.status, i.attempts, i.last_error, i.next_attempt_at,
       COALESCE(p.tenant_id::text, i.headers->>'tenant_id', i.internal_payload->>'tenant_id', '')::text AS tenant_id,
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
), inventory AS (
    SELECT * FROM historical_rows
    UNION ALL
    SELECT * FROM inbox_rows
    UNION ALL
    SELECT * FROM fallback_rows
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
