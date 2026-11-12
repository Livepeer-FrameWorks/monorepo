CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_auth_authz_codes_consumed_range
    ON commodore.auth_authorization_codes (consumed_at ASC) WHERE consumed_at IS NOT NULL;
