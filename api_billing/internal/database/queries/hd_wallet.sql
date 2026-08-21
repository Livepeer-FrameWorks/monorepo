-- name: AllocateHDWalletDerivationIndex :one
UPDATE purser.hd_wallet_state
SET next_index = next_index + 1, updated_at = NOW()
WHERE id = 1
RETURNING next_index - 1 AS derivation_index, xpub;

-- name: GetHDWalletXpub :one
SELECT xpub
FROM purser.hd_wallet_state
WHERE id = 1;

-- name: InitializeHDWalletState :exec
INSERT INTO purser.hd_wallet_state (id, xpub)
VALUES (1, sqlc.arg(xpub));

-- name: LockHDWalletState :one
SELECT xpub, next_index
FROM purser.hd_wallet_state
WHERE id = 1
FOR UPDATE;

-- name: InitializeRotatedHDWalletState :exec
INSERT INTO purser.hd_wallet_state (id, xpub, network, next_index)
VALUES (1, sqlc.arg(xpub), sqlc.arg(network), 0);

-- name: RotateHDWalletState :exec
UPDATE purser.hd_wallet_state
SET xpub = sqlc.arg(xpub), network = sqlc.arg(network), updated_at = NOW()
WHERE id = 1;

-- name: UpsertHDWalletState :exec
INSERT INTO purser.hd_wallet_state (id, xpub, network, next_index)
VALUES (1, sqlc.arg(xpub), sqlc.arg(network), 0)
ON CONFLICT (id) DO UPDATE
SET xpub = EXCLUDED.xpub, network = EXCLUDED.network, updated_at = NOW();

-- name: CreateCryptoWallet :exec
INSERT INTO purser.crypto_wallets (
    id, tenant_id, purpose, invoice_id, expected_amount_cents,
    asset, network, wallet_address, derivation_index, derivation_xpub, expires_at,
    expected_amount_base_units, quoted_price_usd, quoted_usd_to_eur_rate,
    quoted_at, quote_source, credited_amount_currency, client_ip,
    tax_document_kind, tax_profile_snapshot
) VALUES (
    sqlc.arg(id)::text::uuid, sqlc.arg(tenant_id)::text::uuid, sqlc.arg(purpose),
    sqlc.narg(invoice_id)::text::uuid, sqlc.narg(expected_amount_cents)::bigint,
    sqlc.arg(asset), sqlc.arg(network), sqlc.arg(wallet_address),
    sqlc.arg(derivation_index), sqlc.arg(derivation_xpub), sqlc.arg(expires_at),
    sqlc.narg(expected_amount_base_units)::text::numeric,
    sqlc.narg(quoted_price_usd)::text::numeric,
    sqlc.narg(quoted_usd_to_eur_rate)::text::numeric,
    sqlc.narg(quoted_at)::timestamptz, sqlc.narg(quote_source)::text,
    sqlc.narg(credited_amount_currency)::text, sqlc.narg(client_ip)::text,
    sqlc.arg(tax_document_kind), sqlc.arg(tax_profile_snapshot)
);

-- name: RegisterDirectDepositCustodyAddress :exec
INSERT INTO purser.crypto_custody_addresses (
    tenant_id, source_kind, source_ref, network, asset, address, derivation_index, derivation_xpub
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'direct_deposit', sqlc.arg(wallet_id)::text::uuid,
    sqlc.arg(network), sqlc.arg(asset), LOWER(sqlc.arg(address)),
    sqlc.arg(derivation_index), sqlc.arg(derivation_xpub)
)
ON CONFLICT (network, asset, address) DO UPDATE
SET updated_at = NOW();
