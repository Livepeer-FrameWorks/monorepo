-- Existing installations had one active xpub and did not retain it per
-- address. Capture it before allowing the active key to rotate.

UPDATE purser.crypto_wallets w
SET derivation_xpub = s.xpub
FROM purser.hd_wallet_state s
WHERE s.id = 1 AND w.derivation_xpub IS NULL;

UPDATE purser.crypto_custody_addresses ca
SET derivation_xpub = w.derivation_xpub
FROM purser.crypto_wallets w
WHERE ca.source_kind = 'direct_deposit'
  AND ca.source_ref = w.id
  AND ca.derivation_xpub IS NULL;

UPDATE purser.crypto_custody_addresses ca
SET derivation_xpub = s.xpub
FROM purser.hd_wallet_state s
WHERE s.id = 1 AND ca.derivation_xpub IS NULL;

UPDATE purser.tenant_subscriptions ts
SET x402_address_xpub = s.xpub
FROM purser.hd_wallet_state s
WHERE s.id = 1
  AND ts.x402_address_index IS NOT NULL
  AND ts.x402_address_xpub IS NULL;
