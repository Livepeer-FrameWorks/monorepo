ALTER TABLE purser.crypto_wallets
    ALTER COLUMN derivation_xpub SET NOT NULL;

ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS crypto_wallets_derivation_index_key;

ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS crypto_wallets_derivation_xpub_derivation_index_key;

ALTER TABLE purser.crypto_wallets
    ADD CONSTRAINT crypto_wallets_derivation_xpub_derivation_index_key
    UNIQUE (derivation_xpub, derivation_index);

ALTER TABLE purser.crypto_custody_addresses
    ALTER COLUMN derivation_xpub SET NOT NULL;

DROP INDEX IF EXISTS purser.idx_tenant_subscriptions_x402_address_index;

CREATE UNIQUE INDEX idx_tenant_subscriptions_x402_address_index
    ON purser.tenant_subscriptions(x402_address_xpub, x402_address_index)
    WHERE x402_address_index IS NOT NULL;

ALTER TABLE purser.tenant_subscriptions
    DROP CONSTRAINT IF EXISTS chk_x402_address_derivation;

ALTER TABLE purser.tenant_subscriptions
    ADD CONSTRAINT chk_x402_address_derivation
    CHECK ((x402_address_index IS NULL) = (x402_address_xpub IS NULL));
