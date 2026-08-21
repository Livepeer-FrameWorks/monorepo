-- name: ListCryptoUnsweptMetrics :many
WITH unswept AS (
    SELECT wallet.network, wallet.asset, wallet.received_amount_base_units AS amount,
           wallet.completed_at AS received_at
    FROM purser.crypto_wallets wallet
    WHERE wallet.status = 'completed' AND wallet.received_amount_base_units > 0
    UNION ALL
    SELECT custody.network, 'USDC', quote.amount_atomic, quote.confirmed_at
    FROM purser.x402_payment_quotes quote
    JOIN purser.crypto_custody_addresses custody
      ON custody.source_kind = 'x402' AND custody.tenant_id = quote.tenant_id
     AND LOWER(custody.address) = LOWER(quote.pay_to)
     AND quote.network = CASE custody.network
        WHEN 'base' THEN 'eip155:8453'
        WHEN 'arbitrum' THEN 'eip155:42161'
        WHEN 'base-sepolia' THEN 'eip155:84532'
        WHEN 'arbitrum-sepolia' THEN 'eip155:421614'
     END
    WHERE quote.status = 'confirmed'
      AND NOT EXISTS (
        SELECT 1 FROM purser.crypto_sweep_sources source
        JOIN purser.crypto_sweep_items item ON item.id = source.item_id
        WHERE source.source_type = 'x402_quote' AND source.source_id = quote.id
          AND item.status = 'confirmed'
      )
)
SELECT network, asset, SUM(amount)::float8 AS amount,
       EXTRACT(EPOCH FROM (NOW() - MIN(received_at)))::float8 AS age_seconds
FROM unswept
GROUP BY network, asset;

-- name: ListCryptoFailedSweepMetrics :many
SELECT network, asset, COUNT(*)::float8 AS failed_count
FROM purser.crypto_sweep_items
WHERE status = 'failed'
GROUP BY network, asset;

-- name: ListX402QuoteMetrics :many
SELECT quote.network,
       (COUNT(*) FILTER (WHERE quote.status = 'confirmed')::float8 / NULLIF(COUNT(*), 0)::float8)::float8 AS conversion_ratio,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (
           ORDER BY EXTRACT(EPOCH FROM (nonce.confirmed_at - nonce.settled_at))
       ) FILTER (WHERE nonce.confirmed_at IS NOT NULL), 0)::float8 AS settlement_latency_seconds
FROM purser.x402_payment_quotes quote
LEFT JOIN purser.x402_nonces nonce ON nonce.quote_id = quote.id
WHERE quote.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY quote.network;

-- name: ListCryptoPendingDepositMetrics :many
SELECT event.network, event.status, COUNT(*)::float8 AS event_count,
       COUNT(*) FILTER (WHERE event.status = 'review_required' AND wallet.purpose = 'invoice')::float8 AS invoice_review_count
FROM purser.crypto_deposit_events event
LEFT JOIN purser.crypto_wallets wallet ON wallet.id = event.wallet_id
WHERE event.status IN ('observed', 'confirmed', 'review_required')
GROUP BY event.network, event.status;

-- name: ListCryptoAccountingAnomalyMetrics :many
SELECT kind, COUNT(*)::float8 AS anomaly_count,
       EXTRACT(EPOCH FROM (NOW() - MIN(first_seen_at)))::float8 AS age_seconds
FROM purser.crypto_accounting_anomalies
WHERE status = 'open'
GROUP BY kind;

-- name: ListCryptoLedgerReversalMetrics :many
SELECT reference_type::text AS reference_type, COUNT(*)::float8 AS reversal_count
FROM purser.balance_transactions
WHERE transaction_type = 'reversal'
  AND reference_type IN ('x402_failed', 'crypto_reorg', 'crypto_invoice_overpayment_reorg')
GROUP BY reference_type;
