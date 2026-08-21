-- name: InsertUsage :exec
INSERT INTO skipper.skipper_usage (
    tenant_id,
    event_type,
    event_count,
    tokens_input,
    tokens_output,
    model,
    provider,
    created_at
)
VALUES (
    sqlc.arg(tenant_id),
    sqlc.arg(event_type)::text,
    sqlc.arg(event_count)::integer,
    sqlc.arg(tokens_input)::integer,
    sqlc.arg(tokens_output)::integer,
    sqlc.narg(model),
    sqlc.narg(provider),
    NOW()
);

-- name: ClaimPendingUsage :many
SELECT id::text AS id,
       tenant_id::text AS tenant_id,
       event_type,
       event_count,
       COALESCE(tokens_input, 0)::integer AS tokens_input,
       COALESCE(tokens_output, 0)::integer AS tokens_output,
       COALESCE(model, '') AS model,
       COALESCE(provider, '') AS provider,
       created_at
FROM skipper.skipper_usage
WHERE published_at IS NULL
  AND (claimed_at IS NULL OR claimed_at < NOW() - INTERVAL '2 minutes')
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT 50;

-- name: MarkUsageClaimed :exec
UPDATE skipper.skipper_usage
SET claimed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: RecordUsagePublicationFailure :exec
UPDATE skipper.skipper_usage
SET claimed_at = NULL,
    attempts = attempts + 1,
    last_error = sqlc.arg(last_error)::text
WHERE id = sqlc.arg(id)::uuid
  AND published_at IS NULL;

-- name: CompleteUsagePublication :exec
UPDATE skipper.skipper_usage
SET published_at = NOW(), claimed_at = NULL, last_error = NULL
WHERE id = sqlc.arg(id)::uuid;
