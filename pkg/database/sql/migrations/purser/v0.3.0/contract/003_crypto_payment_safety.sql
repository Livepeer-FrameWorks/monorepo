-- Finalize the crypto review state after every writer understands it.

ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS chk_wallet_status_v3_compat;
ALTER TABLE purser.crypto_wallets
    ADD CONSTRAINT chk_wallet_status CHECK (
        status IN ('pending', 'confirming', 'review_required', 'completed', 'swept', 'expired')
    );
