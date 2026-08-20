CREATE TABLE IF NOT EXISTS commodore.wallet_auth_challenges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_address VARCHAR(128) NOT NULL,
    chain_id BIGINT NOT NULL,
    message_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_wallet_auth_challenges_lookup
    ON commodore.wallet_auth_challenges(wallet_address, message_hash)
    WHERE consumed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_commodore_wallet_auth_challenges_expiry
    ON commodore.wallet_auth_challenges(expires_at)
    WHERE consumed_at IS NULL;
