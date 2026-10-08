CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_users_verification_token
    ON commodore.users(verification_token)
    WHERE verification_token IS NOT NULL AND verified = false;
