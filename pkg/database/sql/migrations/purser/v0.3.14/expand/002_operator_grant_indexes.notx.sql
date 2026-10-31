CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grants_expiry
ON purser.subscription_operator_grants(expires_at ASC) WHERE expires_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grants_listing
ON purser.subscription_operator_grants(granted_at DESC, subscription_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_operator_grants_expiry_retry
ON purser.subscription_operator_grants(expiry_attempted_at ASC NULLS FIRST, expires_at ASC)
WHERE expires_at IS NOT NULL;
