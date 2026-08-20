-- Retain the exact public derivation key for every issued custody address.
-- Replacing the active xpub must never make old addresses impossible to sweep.

ALTER TABLE purser.crypto_wallets
    ADD COLUMN IF NOT EXISTS derivation_xpub TEXT;

ALTER TABLE purser.crypto_custody_addresses
    ADD COLUMN IF NOT EXISTS derivation_xpub TEXT;

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS x402_address_xpub TEXT;
