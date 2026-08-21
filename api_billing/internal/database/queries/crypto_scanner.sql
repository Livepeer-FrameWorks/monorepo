-- name: EnsureCryptoScanCursor :exec
INSERT INTO purser.crypto_scan_cursors (network, next_block, safe_head_block, updated_at)
VALUES (sqlc.arg(network), sqlc.arg(next_block), sqlc.arg(safe_head_block), NOW())
ON CONFLICT (network) DO NOTHING;

-- name: GetCryptoScanCursor :one
SELECT next_block, last_scanned_block, last_scanned_block_hash
FROM purser.crypto_scan_cursors
WHERE network = sqlc.arg(network);

-- name: ListKnownCryptoDepositAddresses :many
SELECT DISTINCT LOWER(wallet_address)::text AS wallet_address
FROM purser.crypto_wallets
WHERE network = sqlc.arg(network);

-- name: UpsertObservedCryptoDeposit :exec
INSERT INTO purser.crypto_deposit_events (
    network, asset, tx_hash, log_index, block_number, block_hash,
    from_address, to_address, amount_base_units, status,
    confirmations, confirmed_at
) VALUES (
    sqlc.arg(network), sqlc.arg(asset), sqlc.arg(tx_hash), sqlc.arg(log_index),
    sqlc.arg(block_number), sqlc.arg(block_hash), sqlc.arg(from_address),
    sqlc.arg(to_address), sqlc.arg(amount_base_units)::text::numeric,
    'confirmed', 1, NOW()
)
ON CONFLICT (network, tx_hash, log_index) DO UPDATE
SET canonical = TRUE, block_hash = EXCLUDED.block_hash,
    block_number = EXCLUDED.block_number,
    confirmations = GREATEST(purser.crypto_deposit_events.confirmations, 1),
    status = CASE
        WHEN purser.crypto_deposit_events.status = 'reorged' THEN 'confirmed'
        ELSE purser.crypto_deposit_events.status
    END,
    reorged_at = NULL;

-- name: AdvanceCryptoScanCursor :execrows
UPDATE purser.crypto_scan_cursors
SET next_block = sqlc.arg(last_scanned_block)::bigint + 1,
    last_scanned_block = sqlc.arg(last_scanned_block)::bigint,
    last_scanned_block_hash = sqlc.arg(last_scanned_block_hash),
    safe_head_block = sqlc.arg(safe_head_block)::bigint,
    lag_blocks = GREATEST(sqlc.arg(safe_head_block)::bigint - sqlc.arg(last_scanned_block)::bigint, 0),
    last_error = NULL, error_count = 0, scanned_at = NOW(), updated_at = NOW()
WHERE network = sqlc.arg(network) AND next_block = sqlc.arg(expected_next_block)::bigint;

-- name: MarkCryptoDepositsReorgedFromBlock :exec
UPDATE purser.crypto_deposit_events
SET canonical = FALSE, status = 'reorged', reorged_at = NOW()
WHERE network = sqlc.arg(network)
  AND block_number >= sqlc.arg(block_number)
  AND status != 'allocated';

-- name: RewindCryptoScanCursor :exec
UPDATE purser.crypto_scan_cursors
SET next_block = sqlc.arg(block_number), last_scanned_block = NULL,
    last_scanned_block_hash = NULL, updated_at = NOW()
WHERE network = sqlc.arg(network);

-- name: UpdateCryptoScannerLag :exec
UPDATE purser.crypto_scan_cursors
SET safe_head_block = sqlc.arg(safe_head_block),
    lag_blocks = GREATEST(sqlc.arg(lag_blocks), 0),
    last_error = NULL, error_count = 0, updated_at = NOW()
WHERE network = sqlc.arg(network);

-- name: RecordCryptoScannerError :exec
INSERT INTO purser.crypto_scan_cursors (network, next_block, last_error, error_count)
VALUES (sqlc.arg(network), 0, sqlc.arg(message), 1)
ON CONFLICT (network) DO UPDATE
SET last_error = EXCLUDED.last_error,
    error_count = purser.crypto_scan_cursors.error_count + 1,
    updated_at = NOW();

-- name: ListConfirmedCryptoDepositAllocations :many
SELECT event.id::text AS event_id, event.tx_hash, event.block_number,
       event.amount_base_units::text AS amount_base_units,
       wallet.id::text AS wallet_id, wallet.tenant_id::text AS tenant_id,
       wallet.purpose, COALESCE(wallet.invoice_id::text, '')::text AS invoice_id,
       wallet.expected_amount_cents, wallet.asset, wallet.network, wallet.wallet_address,
       COALESCE(wallet.expected_amount_base_units::text, '')::text AS expected_amount_base_units,
       COALESCE(wallet.quoted_price_usd::text, '')::text AS quoted_price_usd,
       COALESCE(wallet.quoted_usd_to_eur_rate::text, '')::text AS quoted_usd_to_eur_rate,
       COALESCE(wallet.quote_source, '')::text AS quote_source,
       COALESCE(wallet.credited_amount_currency, '')::text AS credited_amount_currency,
       COALESCE(wallet.client_ip, '')::text AS client_ip, wallet.expires_at,
       COALESCE(invoice.amount::float8, 0)::float8 AS invoice_amount,
       COALESCE(invoice.currency, '')::text AS invoice_currency
FROM purser.crypto_deposit_events event
JOIN purser.crypto_wallets wallet
  ON wallet.network = event.network
 AND LOWER(wallet.wallet_address) = LOWER(event.to_address)
 AND wallet.asset = event.asset
LEFT JOIN purser.billing_invoices invoice
  ON invoice.id = wallet.invoice_id AND invoice.tenant_id = wallet.tenant_id
WHERE event.canonical AND event.status = 'confirmed'
  AND wallet.status IN ('pending', 'confirming')
ORDER BY event.block_number, event.log_index
LIMIT 100;

-- name: MarkCryptoDepositAllocationReview :exec
UPDATE purser.crypto_deposit_events
SET status = 'review_required', wallet_id = sqlc.arg(wallet_id)::text::uuid,
    allocation_error = sqlc.arg(allocation_error)
WHERE id = sqlc.arg(event_id)::text::uuid AND status = 'confirmed';
