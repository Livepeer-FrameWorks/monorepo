-- Unsent ending notices, for the platform-wide stale-notice discard.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grant_notices_unsent_ending
ON purser.operator_grant_notices(ends_at ASC)
WHERE kind = 'ending' AND notified_at IS NULL;

-- A tenant's notices by kind and end time, for the operator resend flow.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grant_notices_tenant
ON purser.operator_grant_notices(tenant_id ASC, kind ASC, ends_at ASC);
