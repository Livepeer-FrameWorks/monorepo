-- name: EnqueueAccountEmail :exec
-- A new request restarts delivery: attempts and the 24h window reset, so a
-- resend after an abandoned row is retried again.
INSERT INTO commodore.account_email_outbox (user_id, tenant_id, purpose)
VALUES (sqlc.arg(user_id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(purpose)::text)
ON CONFLICT (user_id, purpose) DO UPDATE SET
    tenant_id = EXCLUDED.tenant_id,
    status = 'pending',
    attempts = 0,
    next_attempt_at = NOW(),
    last_error = NULL,
    lease_token = NULL,
    created_at = NOW(),
    updated_at = NOW(),
    completed_at = NULL;

-- name: AbandonExpiredAccountEmails :execrows
UPDATE commodore.account_email_outbox
SET status = 'abandoned', updated_at = NOW(), lease_token = NULL
WHERE status = 'pending'
  AND created_at < NOW() - (sqlc.arg(window_ms)::bigint * INTERVAL '1 millisecond');

-- name: ClaimAccountEmailBatch :many
SELECT user_id::text AS user_id,
       tenant_id::text AS tenant_id,
       purpose,
       attempts
FROM commodore.account_email_outbox
WHERE status = 'pending'
  AND next_attempt_at <= NOW()
ORDER BY next_attempt_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size);

-- name: LeaseAccountEmail :one
UPDATE commodore.account_email_outbox
SET next_attempt_at = NOW() + (sqlc.arg(lease_ms)::bigint * INTERVAL '1 millisecond'),
    lease_token = gen_random_uuid()::text,
    updated_at = NOW()
WHERE user_id = sqlc.arg(user_id)::uuid
  AND purpose = sqlc.arg(purpose)::text
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'pending'
RETURNING lease_token;

-- name: CompleteAccountEmail :exec
UPDATE commodore.account_email_outbox
SET status = 'completed', completed_at = NOW(), updated_at = NOW(), last_error = NULL
WHERE user_id = sqlc.arg(user_id)::uuid
  AND purpose = sqlc.arg(purpose)::text
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'pending'
  AND (sqlc.arg(lease_token)::text = '' OR lease_token = sqlc.arg(lease_token)::text);

-- name: FailAccountEmail :exec
UPDATE commodore.account_email_outbox
SET attempts = sqlc.arg(attempts),
    next_attempt_at = NOW() + (sqlc.arg(backoff_ms)::bigint * INTERVAL '1 millisecond'),
    last_error = sqlc.arg(last_error)::text,
    updated_at = NOW()
WHERE user_id = sqlc.arg(user_id)::uuid
  AND purpose = sqlc.arg(purpose)::text
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'pending'
  AND (sqlc.arg(lease_token)::text = '' OR lease_token = sqlc.arg(lease_token)::text);

-- name: GetAccountEmailRecipient :one
SELECT COALESCE(email::text, ''::text)::text AS email,
       COALESCE(verified, false)::boolean AS verified
FROM commodore.users
WHERE id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: SetUnverifiedUserVerificationToken :execrows
-- Only the link in the most recent send is valid: each attempt rotates the
-- stored hash before handing the raw token to SMTP.
UPDATE commodore.users
SET verification_token = sqlc.arg(verification_token)::text,
    token_expires_at = sqlc.arg(token_expires_at)::timestamp,
    updated_at = NOW()
WHERE id = sqlc.arg(user_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND COALESCE(verified, false) = false;
