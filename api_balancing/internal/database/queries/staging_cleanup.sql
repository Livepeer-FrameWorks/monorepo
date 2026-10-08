-- name: ClaimStagingCleanupItems :many
-- Leases up to batch_limit due, unleased rows under one caller-minted lease token. A non-negative claim_bucket limits
-- the claim to the rows whose key hashes into that bucket of claim_buckets, so concurrent workers that pick different
-- buckets lock disjoint rows; -1 claims from any bucket. There is no ORDER BY: every due row is eligible, and an
-- unordered LIMIT lets the scan stop once it has the batch instead of reading and sorting every due row.
UPDATE foghorn.staging_cleanup_queue q
SET leased_until = NOW() + sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second',
    lease_token = sqlc.arg(lease_token)::text
WHERE q.object_key IN (
    SELECT object_key FROM foghorn.staging_cleanup_queue
    WHERE next_attempt_at <= NOW()
      AND (leased_until IS NULL OR leased_until <= NOW())
      AND (sqlc.arg(claim_bucket)::bigint < 0
           OR mod(hashtext(object_key)::bigint + 2147483648, sqlc.arg(claim_buckets)::bigint) = sqlc.arg(claim_bucket)::bigint)
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING q.object_key, q.attempts, COALESCE(q.backend_id, '')::text AS backend_id;

-- name: FailStagingCleanupItems :execrows
-- Releases the lease on each listed row and schedules its retry after base * min(attempts + 1, 30), recording its
-- error. Fenced on the lease token, so rows another worker re-claimed after this lease expired are left alone.
UPDATE foghorn.staging_cleanup_queue q
SET attempts = q.attempts + 1,
    next_attempt_at = NOW() + LEAST(q.attempts + 1, 30) * sqlc.arg(backoff_base_seconds)::bigint * INTERVAL '1 second',
    leased_until = NULL, lease_token = NULL, last_error = f.last_error
FROM (SELECT unnest(sqlc.arg(object_keys)::text[]) AS object_key,
             unnest(sqlc.arg(last_errors)::text[]) AS last_error) f
WHERE q.object_key = f.object_key AND q.lease_token = sqlc.arg(lease_token)::text;

-- name: DeleteStagingCleanupItems :execrows
-- Removes the listed rows once their objects are deleted, fenced on the lease token like FailStagingCleanupItems.
DELETE FROM foghorn.staging_cleanup_queue
WHERE object_key = ANY(sqlc.arg(object_keys)::text[]) AND lease_token = sqlc.arg(lease_token)::text;
