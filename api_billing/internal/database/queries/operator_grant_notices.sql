-- name: EnqueueOperatorGrantEndingNotices :execrows
-- Platform-wide: every grant in force that ends within the lead time gets one
-- ending notice for that end time. Closed accounts get none.
INSERT INTO purser.operator_grant_notices (tenant_id, subscription_id, kind, ends_at)
SELECT ts.tenant_id, g.subscription_id, 'ending', g.expires_at
FROM purser.subscription_operator_grants g
JOIN purser.tenant_subscriptions ts ON ts.id = g.subscription_id
WHERE g.expires_at > NOW()
  AND g.expires_at <= NOW() + sqlc.arg(lead_seconds)::bigint * INTERVAL '1 second'
  AND ts.status != 'cancelled'
ON CONFLICT (subscription_id, kind, ends_at) DO NOTHING;

-- name: EnqueueOperatorGrantEndedNotice :exec
-- Written in the transaction that removes the grant.
INSERT INTO purser.operator_grant_notices (tenant_id, subscription_id, kind, ends_at)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(subscription_id)::text::uuid, 'ended', sqlc.arg(ended_at)::timestamptz)
ON CONFLICT (subscription_id, kind, ends_at) DO NOTHING;

-- name: DiscardStaleOperatorGrantEndingNotices :execrows
-- Platform-wide: an unsent ending notice whose grant no longer ends at that
-- time (extended, shortened or already ended) has nothing left to announce.
DELETE FROM purser.operator_grant_notices n
WHERE n.kind = 'ending' AND n.notified_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM purser.subscription_operator_grants g
      WHERE g.subscription_id = n.subscription_id AND g.expires_at = n.ends_at AND g.expires_at > NOW()
  );

-- name: ListOperatorGrantNotices :many
-- Platform-wide queue with the Free fallback notice backoff.
SELECT n.id::text AS id
FROM purser.operator_grant_notices n
WHERE n.notified_at IS NULL AND n.abandoned_at IS NULL
  AND (n.attempted_at IS NULL OR n.attempted_at <= NOW() - (LEAST(720, 5 * power(2, LEAST(n.failures, 8))) * INTERVAL '1 minute'))
ORDER BY n.attempted_at ASC NULLS FIRST, n.ends_at ASC, n.id ASC
LIMIT 100;

-- name: ClaimOperatorGrantNotice :one
-- The claim is committed before SMTP; no billing row lock spans external I/O.
-- plan_name is the plan the account is on after the end: for an ending
-- notice the grant's fallback when the grant assigned the current tier,
-- otherwise the current tier.
UPDATE purser.operator_grant_notices n
SET attempted_at = NOW(), failures = n.failures + 1
FROM purser.tenant_subscriptions ts
JOIN purser.billing_tiers current_tier ON current_tier.id = ts.tier_id
LEFT JOIN purser.subscription_operator_grants g ON g.subscription_id = ts.id
LEFT JOIN purser.billing_tiers fallback ON fallback.id = g.fallback_tier_id
WHERE n.id = sqlc.arg(id)::text::uuid
  AND ts.id = n.subscription_id
  AND n.notified_at IS NULL AND n.abandoned_at IS NULL
  AND (n.attempted_at IS NULL OR n.attempted_at <= NOW() - (LEAST(720, 5 * power(2, LEAST(n.failures, 8))) * INTERVAL '1 minute'))
RETURNING n.tenant_id::text AS tenant_id, n.kind, n.ends_at,
          n.attempted_at::timestamptz AS attempted_at,
          COALESCE(ts.billing_email, '')::text AS billing_email,
          COALESCE(ts.billing_name, '')::text AS billing_name,
          COALESCE(CASE WHEN n.kind = 'ending' AND fallback.id IS NOT NULL
                         AND ts.tier_id = COALESCE(g.assigned_tier_id, ts.tier_id)
                    THEN COALESCE(NULLIF(fallback.display_name, ''), fallback.tier_name) END,
                   NULLIF(current_tier.display_name, ''), current_tier.tier_name)::text AS plan_name;

-- name: CompleteOperatorGrantNotice :exec
-- A stale sender cannot acknowledge a newer attempt.
UPDATE purser.operator_grant_notices
SET notified_at = NOW(), error = NULL
WHERE id = sqlc.arg(id)::text::uuid
  AND attempted_at = sqlc.arg(attempted_at)::timestamptz;

-- name: RecordOperatorGrantNoticeFailure :exec
UPDATE purser.operator_grant_notices
SET error = sqlc.arg(failure)::text,
    abandoned_at = CASE WHEN failures >= 5 THEN NOW() ELSE abandoned_at END
WHERE id = sqlc.arg(id)::text::uuid
  AND attempted_at = sqlc.arg(attempted_at)::timestamptz;

-- name: CountRepeatedOperatorGrantNoticeFailures :one
-- Platform-wide recovery metric; one failed attempt alone is not stuck. An
-- abandoned notice waits for an operator requeue and is not retried, so it
-- is not counted here; cluster doctor --deep lists it.
SELECT COUNT(*) FROM purser.operator_grant_notices
WHERE notified_at IS NULL AND error IS NOT NULL AND failures > 1 AND abandoned_at IS NULL;

-- name: GetAbandonedOperatorGrantNotice :one
-- The tenant's newest abandoned notice; its kind and end identify it when the
-- operator confirms the requeue.
SELECT n.kind, n.ends_at, COALESCE(n.error, '')::text AS last_error,
       COALESCE(ts.billing_email, '')::text AS recipient
FROM purser.operator_grant_notices n
JOIN purser.tenant_subscriptions ts ON ts.id = n.subscription_id
WHERE n.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND n.notified_at IS NULL AND n.abandoned_at IS NOT NULL
ORDER BY n.ends_at DESC, n.kind ASC
LIMIT 1;

-- name: GetOperatorGrantNoticeRecovery :one
SELECT n.subscription_id::text AS subscription_id,
       (n.notified_at IS NOT NULL)::boolean AS notified,
       (n.abandoned_at IS NOT NULL)::boolean AS abandoned,
       ts.status,
       COALESCE(ts.billing_email, '')::text AS recipient
FROM purser.operator_grant_notices n
JOIN purser.tenant_subscriptions ts ON ts.id = n.subscription_id
WHERE n.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND n.kind = sqlc.arg(kind)::text
  AND n.ends_at = sqlc.arg(ends_at)::timestamptz;

-- name: RequeueAbandonedOperatorGrantNotice :execrows
-- Only one of several concurrent confirmations finds the notice abandoned.
UPDATE purser.operator_grant_notices
SET abandoned_at = NULL, attempted_at = NULL, failures = 0, error = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND kind = sqlc.arg(kind)::text
  AND ends_at = sqlc.arg(ends_at)::timestamptz
  AND notified_at IS NULL AND abandoned_at IS NOT NULL;
