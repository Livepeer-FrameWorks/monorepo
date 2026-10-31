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
SET status = 'processing', claimed_at = NOW(),
    lease_token = sqlc.arg(lease_token)::text::uuid, updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CompleteProviderWebhook :execresult
UPDATE purser.provider_webhook_inbox
SET status = 'processed', processed_at = NOW(), claimed_at = NULL,
    lease_token = NULL, last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: FailProviderWebhook :execresult
UPDATE purser.provider_webhook_inbox
SET status = CASE WHEN sqlc.arg(waiting)::boolean THEN 'pending' ELSE 'failed' END,
    attempts = attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 0 ELSE 1 END,
    wait_attempts = wait_attempts + CASE WHEN sqlc.arg(waiting)::boolean THEN 1 ELSE 0 END,
    unresolved_since = COALESCE(unresolved_since, NOW()),
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * INTERVAL '1 millisecond'),
    claimed_at = NULL, lease_token = NULL,
    last_error = sqlc.arg(last_error)::text, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);

-- name: ProviderWebhookInboxRepeatedFailures :one
-- Service-wide recovery inventory; tenant filtering would hide stuck work.
SELECT COUNT(*)::bigint AS items
FROM purser.provider_webhook_inbox
WHERE status IN ('pending', 'failed', 'processing')
  AND (attempts > 1 OR unresolved_since < NOW() - INTERVAL '14 days');

-- name: ListCheckoutRecovery :many
-- Platform-operator inventory includes unbound callbacks; a supplied tenant narrows to its reservations.
SELECT i.id::text, COALESCE(p.provider, i.headers->>'provider', i.provider)::text AS provider,
       i.event_key, i.status, i.attempts, i.last_error, i.next_attempt_at,
       COALESCE(p.tenant_id::text, i.headers->>'tenant_id', '')::text AS tenant_id,
       COALESCE(p.id::text, upgrade.id::text, i.headers->>'reference_id', '')::text AS reservation_id,
       COALESCE(p.expires_at, p.created_at + INTERVAL '24 hours') AS expires_at,
       COALESCE(p.amount_cents, upgrade.base_cents, NULLIF(i.headers->>'amount_cents', '')::bigint, 0)::bigint AS amount_cents,
       COALESCE(p.currency, i.headers->>'currency', '')::text AS currency,
       COALESCE(p.status, upgrade.state, i.headers->>'reference_kind', '')::text AS reservation_status
FROM purser.provider_webhook_inbox i
LEFT JOIN purser.payment_provider_intents p
  ON i.provider = 'checkout_recovery'
 AND p.id::text = convert_from(i.raw_payload, 'UTF8')::jsonb->>'checkout_reservation_id'
 AND p.tenant_id::text = convert_from(i.raw_payload, 'UTF8')::jsonb->>'tenant_id'
LEFT JOIN purser.subscription_tier_upgrades upgrade
  ON i.provider = 'tier_upgrade'
 AND upgrade.id::text = convert_from(i.raw_payload, 'UTF8')::jsonb->>'id'
 AND upgrade.tenant_id::text = i.headers->>'tenant_id'
WHERE i.status <> 'processed'
  AND (sqlc.arg(tenant_id)::text = ''
       OR COALESCE(p.tenant_id::text, i.headers->>'tenant_id', '') = sqlc.arg(tenant_id)::text)
ORDER BY i.created_at, i.id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: RequestCheckoutRecovery :exec
INSERT INTO purser.provider_webhook_inbox (
    provider, event_key, headers, raw_payload, next_attempt_at
)
SELECT 'checkout_recovery', 'checkout-expiry:' || id::text,
       jsonb_build_object('recovery_reason', sqlc.arg(reason)::text),
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
    status = 'pending', claimed_at = NULL, lease_token = NULL, updated_at = NOW();

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
    status = 'pending', next_attempt_at = NOW(), processed_at = NULL,
    attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.attempts END,
    wait_attempts = CASE WHEN inbox.status = 'processed' THEN 0 ELSE inbox.wait_attempts END,
    unresolved_since = CASE WHEN inbox.status = 'processed' THEN NULL ELSE inbox.unresolved_since END,
    claimed_at = NULL, lease_token = NULL, updated_at = NOW();
