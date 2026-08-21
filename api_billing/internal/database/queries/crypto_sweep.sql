-- name: CountCurrentEUVATRates :one
SELECT COUNT(*)
FROM purser.vat_rate_periods
WHERE effective_from <= CURRENT_DATE
  AND (effective_until IS NULL OR effective_until > CURRENT_DATE);

-- name: CountOpenCryptoAccountingAnomalies :one
SELECT COUNT(*) FROM purser.crypto_accounting_anomalies WHERE status = 'open';

-- name: GetCryptoScannerHealth :one
SELECT scanned_at, last_error, lag_blocks
FROM purser.crypto_scan_cursors
WHERE network = sqlc.arg(network);

-- name: ListCryptoSweepCandidates :many
SELECT address.id::text AS custody_id, wallet.id::text AS wallet_id,
       address.asset, LOWER(address.address)::text AS address,
       address.derivation_index, address.derivation_xpub,
       'direct_wallet'::text AS source_type, wallet.id::text AS source_id,
       wallet.received_amount_base_units::text AS amount_base_units
FROM purser.crypto_custody_addresses address
JOIN purser.crypto_wallets wallet
  ON address.source_kind = 'direct_deposit' AND address.source_ref = wallet.id
WHERE address.network = sqlc.arg(network) AND wallet.status = 'completed'
  AND wallet.received_amount_base_units > 0
  AND NOT EXISTS (
      SELECT 1 FROM purser.crypto_sweep_sources source
      WHERE source.source_type = 'direct_wallet' AND source.source_id = wallet.id
        AND source.claim_status IN ('claimed', 'consumed', 'quarantined')
  )
UNION ALL
SELECT address.id::text AS custody_id, ''::text AS wallet_id,
       address.asset, LOWER(address.address)::text AS address,
       address.derivation_index, address.derivation_xpub,
       'x402_quote'::text AS source_type, quote.id::text AS source_id,
       quote.amount_atomic::text AS amount_base_units
FROM purser.crypto_custody_addresses address
JOIN purser.x402_payment_quotes quote
  ON address.source_kind = 'x402' AND quote.tenant_id = address.tenant_id
 AND LOWER(quote.pay_to) = LOWER(address.address)
 AND quote.network = sqlc.arg(caip2_network)
WHERE address.network = sqlc.arg(network) AND address.asset = 'USDC'
  AND quote.status = 'confirmed'
  AND NOT EXISTS (
      SELECT 1 FROM purser.crypto_sweep_sources source
      WHERE source.source_type = 'x402_quote' AND source.source_id = quote.id
        AND source.claim_status IN ('claimed', 'consumed', 'quarantined')
  )
ORDER BY 1, 7;

-- name: InsertCryptoSweepBatch :exec
INSERT INTO purser.crypto_sweep_batches (
    id, manifest_version, network, treasury_address, snapshot_block,
    snapshot_block_hash, manifest_checksum, expires_at, created_by
) VALUES (
    sqlc.arg(id)::text::uuid, sqlc.arg(manifest_version), sqlc.arg(network),
    sqlc.arg(treasury_address), sqlc.arg(snapshot_block), sqlc.arg(snapshot_block_hash),
    sqlc.arg(manifest_checksum), sqlc.arg(expires_at), NULLIF(sqlc.arg(created_by), '')::uuid
);

-- name: InsertCryptoSweepItem :exec
INSERT INTO purser.crypto_sweep_items (
    id, batch_id, custody_address_id, wallet_id, network, asset,
    source_address, derivation_index, destination_address, amount_base_units,
    chain_id, asset_contract, source_nonce, max_fee_per_gas,
    max_priority_fee_per_gas, gas_limit, authorization_nonce,
    authorization_after, authorization_before
) VALUES (
    sqlc.arg(id)::text::uuid, sqlc.arg(batch_id)::text::uuid,
    sqlc.arg(custody_address_id)::text::uuid, NULLIF(sqlc.arg(wallet_id), '')::uuid,
    sqlc.arg(network), sqlc.arg(asset), sqlc.arg(source_address), sqlc.arg(derivation_index),
    sqlc.arg(destination_address), sqlc.arg(amount_base_units)::text::numeric,
    sqlc.arg(chain_id), NULLIF(sqlc.arg(asset_contract), ''), sqlc.arg(source_nonce),
    sqlc.arg(max_fee_per_gas), sqlc.arg(max_priority_fee_per_gas), sqlc.arg(gas_limit),
    NULLIF(sqlc.arg(authorization_nonce), ''), NULLIF(sqlc.arg(authorization_after), 0),
    NULLIF(sqlc.arg(authorization_before), 0)
);

-- name: InsertCryptoSweepSource :exec
INSERT INTO purser.crypto_sweep_sources (
    item_id, source_type, source_id, amount_base_units, claimed_by, claim_reason
) VALUES (
    sqlc.arg(item_id)::text::uuid, sqlc.arg(source_type), sqlc.arg(source_id)::text::uuid,
    sqlc.arg(amount_base_units)::text::numeric, NULLIF(sqlc.arg(claimed_by), '')::uuid, 'sweep plan'
);

-- name: InsertCryptoSweepPlannedEvent :exec
INSERT INTO purser.crypto_sweep_events (batch_id, event_type, actor_id, payload)
VALUES (
    sqlc.arg(batch_id)::text::uuid, 'planned', NULLIF(sqlc.arg(actor_id), '')::uuid,
    jsonb_build_object('checksum', sqlc.arg(checksum)::text, 'items', sqlc.arg(item_count)::int)
);

-- name: GetPersistedCryptoSweepBatch :one
SELECT manifest_checksum, network, LOWER(treasury_address)::text AS treasury_address,
       snapshot_block, snapshot_block_hash, status, expires_at
FROM purser.crypto_sweep_batches
WHERE id = sqlc.arg(batch_id)::text::uuid;

-- name: LockCryptoSweepRelayItem :one
SELECT relay_transaction, tx_hash, status
FROM purser.crypto_sweep_items
WHERE id = sqlc.arg(item_id)::text::uuid
FOR UPDATE;

-- name: EnsureCryptoSweepRelayerNonce :exec
INSERT INTO purser.crypto_sweep_relayer_nonces (network, next_nonce)
VALUES (sqlc.arg(network), sqlc.arg(chain_nonce))
ON CONFLICT (network) DO NOTHING;

-- name: ReserveCryptoSweepRelayerNonce :one
UPDATE purser.crypto_sweep_relayer_nonces
SET next_nonce = GREATEST(next_nonce, sqlc.arg(chain_nonce)) + 1, updated_at = NOW()
WHERE network = sqlc.arg(network)
RETURNING next_nonce - 1;

-- name: MarkUSDCryptoSweepItemBroadcast :exec
UPDATE purser.crypto_sweep_items
SET signed_payload = sqlc.arg(signed_payload), relay_transaction = sqlc.arg(relay_transaction),
    tx_hash = sqlc.arg(tx_hash), status = 'broadcast', broadcast_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(item_id)::text::uuid AND status IN ('planned', 'signed', 'broadcast');

-- name: GetPersistedCryptoSweepItem :one
SELECT status, tx_hash, relay_transaction
FROM purser.crypto_sweep_items
WHERE id = sqlc.arg(item_id)::text::uuid AND batch_id = sqlc.arg(batch_id)::text::uuid;

-- name: MarkETHCryptoSweepItemBroadcast :exec
UPDATE purser.crypto_sweep_items
SET signed_payload = sqlc.arg(signed_payload), tx_hash = sqlc.arg(tx_hash),
    status = 'broadcast', broadcast_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(item_id)::text::uuid AND status IN ('planned', 'signed', 'broadcast');

-- name: RecordCryptoSweepBroadcast :exec
WITH updated AS (
    UPDATE purser.crypto_sweep_batches SET status = 'broadcast', updated_at = NOW()
    WHERE id = sqlc.arg(batch_id)::text::uuid RETURNING id
)
INSERT INTO purser.crypto_sweep_events (batch_id, event_type, actor_id, payload)
SELECT id, 'broadcast_requested', NULLIF(sqlc.arg(actor_id), '')::uuid,
       jsonb_build_object('bundle_checksum', sqlc.arg(bundle_checksum)::text)
FROM updated;

-- name: GetCryptoSweepBatchNetwork :one
SELECT network FROM purser.crypto_sweep_batches WHERE id = sqlc.arg(batch_id)::text::uuid;

-- name: ListCryptoSweepReconcileItems :many
SELECT id::text AS item_id, COALESCE(wallet_id::text, '')::text AS wallet_id, tx_hash, status
FROM purser.crypto_sweep_items
WHERE batch_id = sqlc.arg(batch_id)::text::uuid
ORDER BY id;

-- name: MarkCryptoSweepItemFailed :exec
UPDATE purser.crypto_sweep_items
SET status = 'failed', failure_reason = 'transaction reverted', updated_at = NOW()
WHERE id = sqlc.arg(item_id)::text::uuid;

-- name: MarkCryptoSweepItemConfirmed :exec
UPDATE purser.crypto_sweep_items
SET status = 'confirmed', confirmed_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(item_id)::text::uuid;

-- name: ConsumeCryptoSweepSources :exec
UPDATE purser.crypto_sweep_sources
SET claim_status = 'consumed', consumed_at = NOW(), updated_at = NOW()
WHERE item_id = sqlc.arg(item_id)::text::uuid AND claim_status IN ('claimed', 'quarantined');

-- name: MarkSweptCryptoWallet :exec
UPDATE purser.crypto_wallets SET status = 'swept', updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid AND status = 'completed';

-- name: RecordCryptoSweepReconciliation :exec
WITH updated AS (
    UPDATE purser.crypto_sweep_batches SET status = sqlc.arg(status), updated_at = NOW()
    WHERE id = sqlc.arg(batch_id)::text::uuid RETURNING id
)
INSERT INTO purser.crypto_sweep_events (batch_id, event_type, actor_id, payload)
SELECT id, 'reconciled', NULLIF(sqlc.arg(actor_id), '')::uuid,
       jsonb_build_object('status', sqlc.arg(status)::text,
          'confirmed', sqlc.arg(confirmed_items)::int,
          'pending', sqlc.arg(pending_items)::int,
          'failed', sqlc.arg(failed_items)::int)
FROM updated;

-- name: GetCryptoSweepReleaseBatch :one
SELECT manifest_checksum, network, expires_at
FROM purser.crypto_sweep_batches WHERE id = sqlc.arg(batch_id)::text::uuid;

-- name: ListCryptoSweepReleaseItems :many
SELECT item.id::text AS item_id, item.asset, LOWER(item.source_address)::text AS source_address,
       item.amount_base_units::text AS amount_base_units, item.source_nonce,
       item.authorization_nonce, item.authorization_before, item.signed_payload,
       item.relay_transaction, item.tx_hash, item.status, item.broadcast_at,
       (SELECT COUNT(*) FROM purser.crypto_sweep_sources source
        WHERE source.item_id = item.id AND source.claim_status = 'claimed') AS claimed_sources
FROM purser.crypto_sweep_items item
WHERE item.batch_id = sqlc.arg(batch_id)::text::uuid
ORDER BY item.id;

-- name: GetCryptoSweepSnapshot :one
SELECT snapshot_block, snapshot_block_hash
FROM purser.crypto_sweep_batches WHERE id = sqlc.arg(batch_id)::text::uuid;

-- name: LockCryptoSweepReleaseItem :one
SELECT status, signed_payload, relay_transaction, tx_hash, broadcast_at
FROM purser.crypto_sweep_items
WHERE id = sqlc.arg(item_id)::text::uuid
FOR UPDATE;

-- name: ReleaseCryptoSweepSources :execrows
UPDATE purser.crypto_sweep_sources
SET claim_status = sqlc.arg(source_status)::varchar(20), released_by = NULLIF(sqlc.arg(released_by), '')::uuid,
    release_reason = sqlc.arg(release_reason),
    released_at = CASE WHEN sqlc.arg(source_status)::varchar(20) = 'released' THEN NOW() ELSE released_at END,
    updated_at = NOW()
WHERE item_id = sqlc.arg(item_id)::text::uuid AND claim_status = 'claimed';

-- name: UpdateReleasedCryptoSweepItem :exec
UPDATE purser.crypto_sweep_items
SET status = sqlc.arg(item_status), failure_reason = sqlc.arg(failure_reason), updated_at = NOW()
WHERE id = sqlc.arg(item_id)::text::uuid AND status NOT IN ('confirmed', 'expired');

-- name: InsertCryptoSweepReleaseEvent :exec
INSERT INTO purser.crypto_sweep_events (batch_id, item_id, event_type, actor_id, payload)
VALUES (
    sqlc.arg(batch_id)::text::uuid, sqlc.arg(item_id)::text::uuid, sqlc.arg(event_type),
    NULLIF(sqlc.arg(actor_id), '')::uuid,
    jsonb_build_object('reason', sqlc.arg(reason)::text, 'evidence', sqlc.arg(evidence)::text)
);

-- name: RecordCryptoSweepReleaseCompleted :exec
WITH updated AS (
    UPDATE purser.crypto_sweep_batches SET status = sqlc.arg(status), updated_at = NOW()
    WHERE id = sqlc.arg(batch_id)::text::uuid RETURNING id
)
INSERT INTO purser.crypto_sweep_events (batch_id, event_type, actor_id, payload)
SELECT id, 'release_completed', NULLIF(sqlc.arg(actor_id), '')::uuid,
       jsonb_build_object('reason', sqlc.arg(reason)::text, 'status', sqlc.arg(status)::text,
          'released', sqlc.arg(released_items)::int, 'quarantined', sqlc.arg(quarantined_items)::int,
          'blocked', sqlc.arg(blocked_items)::int)
FROM updated;

-- name: ListExpiredUnsignedCryptoSweepBatches :many
SELECT DISTINCT batch.id::text AS batch_id
FROM purser.crypto_sweep_batches batch
JOIN purser.crypto_sweep_items item ON item.batch_id = batch.id
WHERE batch.network = sqlc.arg(network) AND batch.expires_at <= NOW()
  AND batch.status IN ('planned', 'signed') AND item.status = 'planned'
  AND item.signed_payload IS NULL AND item.relay_transaction IS NULL
  AND item.tx_hash IS NULL AND item.broadcast_at IS NULL;

-- name: GetCryptoSweepManifestChecksum :one
SELECT manifest_checksum FROM purser.crypto_sweep_batches WHERE id = sqlc.arg(batch_id)::text::uuid;

-- name: GetX402MutationResolutionState :one
SELECT quote_id::text AS quote_id, operation, status
FROM purser.x402_mutation_results
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND idempotency_key = sqlc.arg(idempotency_key);

-- name: CompleteX402MutationResult :execrows
UPDATE purser.x402_mutation_results
SET status = 'completed', result = sqlc.arg(result), content_type = NULLIF(sqlc.arg(content_type), ''),
    status_code = sqlc.arg(status_code), completed_at = NOW(),
    resolved_by = NULLIF(sqlc.arg(resolved_by), '')::uuid, resolved_at = NOW(),
    review_reason = sqlc.arg(review_reason), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key) AND status <> 'completed';

-- name: ReviewX402MutationResult :execrows
UPDATE purser.x402_mutation_results
SET status = 'operator_review', review_reason = sqlc.arg(review_reason),
    resolved_by = NULLIF(sqlc.arg(resolved_by), '')::uuid, resolved_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key) AND status <> 'completed';
