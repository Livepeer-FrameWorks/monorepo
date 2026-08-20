-- Restart-safe RPC observation and the durable inventory for the manual,
-- offline-signing sweep ceremony.

CREATE TABLE IF NOT EXISTS purser.crypto_scan_cursors (
    network VARCHAR(20) PRIMARY KEY,
    next_block BIGINT NOT NULL CHECK (next_block >= 0),
    last_scanned_block BIGINT,
    last_scanned_block_hash VARCHAR(66),
    safe_head_block BIGINT,
    lag_blocks BIGINT,
    last_error TEXT,
    error_count INTEGER NOT NULL DEFAULT 0,
    scanned_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_deposit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    tx_hash VARCHAR(66) NOT NULL,
    log_index BIGINT NOT NULL DEFAULT -1,
    block_number BIGINT NOT NULL,
    block_hash VARCHAR(66) NOT NULL,
    from_address VARCHAR(42),
    to_address VARCHAR(42) NOT NULL,
    amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
    canonical BOOLEAN NOT NULL DEFAULT TRUE,
    status VARCHAR(24) NOT NULL DEFAULT 'observed' CHECK (
        status IN ('observed', 'confirmed', 'allocated', 'review_required', 'reorged')
    ),
    wallet_id UUID REFERENCES purser.crypto_wallets(id),
    confirmations INTEGER NOT NULL DEFAULT 0,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at TIMESTAMPTZ,
    allocated_at TIMESTAMPTZ,
    reorged_at TIMESTAMPTZ,
    last_canonical_checked_at TIMESTAMPTZ,
    allocation_error TEXT,
    UNIQUE(network, tx_hash, log_index)
);

CREATE INDEX IF NOT EXISTS idx_crypto_deposit_events_destination
    ON purser.crypto_deposit_events(network, to_address, status);
CREATE INDEX IF NOT EXISTS idx_crypto_deposit_events_allocation
    ON purser.crypto_deposit_events(status, block_number)
    WHERE canonical AND status IN ('observed', 'confirmed', 'review_required');

CREATE TABLE IF NOT EXISTS purser.crypto_custody_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    source_kind VARCHAR(20) NOT NULL CHECK (source_kind IN ('direct_deposit', 'x402')),
    source_ref UUID,
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    address VARCHAR(42) NOT NULL,
    derivation_index INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(network, asset, address),
    UNIQUE(source_kind, source_ref, network, asset)
);

INSERT INTO purser.crypto_custody_addresses (
    tenant_id, source_kind, source_ref, network, asset, address, derivation_index
)
SELECT tenant_id, 'direct_deposit', id, network, asset, LOWER(wallet_address), derivation_index
FROM purser.crypto_wallets
WHERE asset IN ('ETH', 'USDC')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_batches (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_version INTEGER NOT NULL,
    network VARCHAR(20) NOT NULL,
    treasury_address VARCHAR(42) NOT NULL,
    snapshot_block BIGINT NOT NULL,
    snapshot_block_hash VARCHAR(66) NOT NULL,
    manifest_checksum VARCHAR(64) NOT NULL UNIQUE,
    status VARCHAR(24) NOT NULL DEFAULT 'planned' CHECK (
        status IN ('planned', 'signed', 'broadcast', 'partially_confirmed', 'confirmed', 'failed', 'expired')
    ),
    expires_at TIMESTAMPTZ NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id UUID NOT NULL REFERENCES purser.crypto_sweep_batches(id),
    custody_address_id UUID NOT NULL REFERENCES purser.crypto_custody_addresses(id),
    wallet_id UUID REFERENCES purser.crypto_wallets(id),
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    source_address VARCHAR(42) NOT NULL,
    derivation_index INTEGER NOT NULL,
    destination_address VARCHAR(42) NOT NULL,
    amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
    chain_id BIGINT NOT NULL,
    asset_contract VARCHAR(42),
    source_nonce BIGINT,
    max_fee_per_gas NUMERIC(78, 0),
    max_priority_fee_per_gas NUMERIC(78, 0),
    gas_limit BIGINT,
    authorization_nonce VARCHAR(66),
    signed_payload TEXT,
    relay_transaction TEXT,
    tx_hash VARCHAR(66),
    status VARCHAR(24) NOT NULL DEFAULT 'planned' CHECK (
        status IN ('planned', 'signed', 'broadcast', 'confirmed', 'failed', 'expired')
    ),
    failure_reason TEXT,
    broadcast_at TIMESTAMPTZ,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(batch_id, custody_address_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_crypto_sweep_items_tx
    ON purser.crypto_sweep_items(network, tx_hash)
    WHERE tx_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_sources (
    item_id UUID NOT NULL REFERENCES purser.crypto_sweep_items(id),
    source_type VARCHAR(20) NOT NULL CHECK (source_type IN ('direct_wallet', 'x402_quote')),
    source_id UUID NOT NULL,
    amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
    PRIMARY KEY(source_type, source_id)
);

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_events (
    id BIGSERIAL PRIMARY KEY,
    batch_id UUID NOT NULL REFERENCES purser.crypto_sweep_batches(id),
    item_id UUID REFERENCES purser.crypto_sweep_items(id),
    event_type VARCHAR(40) NOT NULL,
    actor_id UUID,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_relayer_nonces (
    network VARCHAR(20) PRIMARY KEY,
    next_nonce BIGINT NOT NULL CHECK (next_nonce >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
