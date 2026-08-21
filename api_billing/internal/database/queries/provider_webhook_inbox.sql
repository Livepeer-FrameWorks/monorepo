-- name: EnqueueProviderWebhook :exec
INSERT INTO purser.provider_webhook_inbox
    (provider, event_key, headers, raw_payload)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, event_key) DO NOTHING;

-- name: ClaimProviderWebhooks :many
SELECT id, provider, headers, raw_payload, attempts
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
SET status = 'failed', attempts = attempts + 1,
    next_attempt_at = NOW() + (sqlc.arg(backoff_milliseconds)::bigint * interval '1 millisecond'),
    claimed_at = NULL, lease_token = NULL, last_error = sqlc.arg(last_error), updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(lease_token)::text = '' OR lease_token::text = sqlc.arg(lease_token)::text);
