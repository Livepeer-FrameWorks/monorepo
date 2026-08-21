-- name: ListPendingCryptoWallets :many
SELECT wallet.id::text AS id, wallet.tenant_id::text AS tenant_id, wallet.purpose,
       COALESCE(wallet.invoice_id::text, '')::text AS invoice_id,
       wallet.expected_amount_cents, wallet.asset, wallet.network, wallet.wallet_address,
       wallet.status, COALESCE(wallet.tx_hash, '')::text AS tx_hash,
       COALESCE(wallet.expected_amount_base_units::text, '')::text AS expected_amount_base_units,
       COALESCE(wallet.quoted_price_usd::text, '')::text AS quoted_price_usd,
       COALESCE(wallet.quoted_usd_to_eur_rate::text, '')::text AS quoted_usd_to_eur_rate,
       COALESCE(wallet.quote_source, '')::text AS quote_source,
       COALESCE(wallet.credited_amount_currency, '')::text AS credited_amount_currency,
       COALESCE(wallet.client_ip, '')::text AS client_ip, wallet.expires_at,
       COALESCE(invoice.amount::float8, 0)::float8 AS invoice_amount,
       COALESCE(invoice.currency, '')::text AS invoice_currency
FROM purser.crypto_wallets wallet
LEFT JOIN purser.billing_invoices invoice
  ON wallet.invoice_id = invoice.id AND invoice.tenant_id = wallet.tenant_id
WHERE wallet.status IN ('pending', 'confirming')
  AND ((wallet.purpose = 'invoice' AND invoice.status IN ('pending', 'overdue'))
       OR wallet.purpose = 'prepaid');

-- name: MarkCryptoWalletForReview :execrows
UPDATE purser.crypto_wallets
SET status = 'review_required', tx_hash = sqlc.arg(tx_hash),
    received_amount_base_units = sqlc.arg(received_amount_base_units)::text::numeric,
    block_number = sqlc.arg(block_number), confirmations = sqlc.arg(confirmations),
    detected_at = COALESCE(detected_at, NOW()), updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('pending', 'confirming');

-- name: CryptoWalletTransactionExists :one
SELECT EXISTS(
    SELECT 1 FROM purser.crypto_wallets
    WHERE network = sqlc.arg(network) AND tx_hash = sqlc.arg(tx_hash)
      AND id != sqlc.arg(wallet_id)::text::uuid
);

-- name: MarkCryptoWalletConfirming :execrows
UPDATE purser.crypto_wallets
SET status = 'confirming', tx_hash = sqlc.arg(tx_hash), confirmations = sqlc.arg(confirmations),
    detected_at = COALESCE(detected_at, sqlc.arg(detected_at)), updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('pending', 'confirming');

-- name: CompleteCryptoWallet :execrows
UPDATE purser.crypto_wallets
SET status = 'completed', tx_hash = sqlc.arg(tx_hash),
    received_amount_base_units = sqlc.arg(received_amount_base_units)::text::numeric,
    block_number = sqlc.arg(block_number), confirmations = sqlc.arg(confirmations),
    detected_at = COALESCE(detected_at, sqlc.arg(completed_at)),
    completed_at = sqlc.arg(completed_at), updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: AllocateCryptoDepositEvent :execrows
UPDATE purser.crypto_deposit_events
SET status = 'allocated', wallet_id = sqlc.arg(wallet_id)::text::uuid,
    allocated_at = NOW(), allocation_error = NULL
WHERE id = sqlc.arg(event_id)::text::uuid AND canonical AND status = 'confirmed';

-- name: ListCompletedCryptoTopupsMissingInvoice :many
SELECT wallet.tenant_id::text AS tenant_id, wallet.credited_amount_cents,
       wallet.credited_amount_currency,
       COALESCE(wallet.quoted_usd_to_eur_rate::text, '')::text AS quoted_usd_to_eur_rate,
       wallet.tx_hash, COALESCE(wallet.client_ip, '')::text AS client_ip, wallet.network
FROM purser.crypto_wallets wallet
WHERE wallet.purpose = 'prepaid' AND wallet.status IN ('completed', 'swept')
  AND wallet.credited_amount_cents > 0 AND wallet.tx_hash IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM purser.simplified_invoices invoice
      WHERE invoice.tenant_id = wallet.tenant_id
        AND invoice.reference_type = 'crypto_payment'
        AND invoice.reference_id = wallet.tx_hash
  )
  AND NOT EXISTS (
      SELECT 1 FROM purser.crypto_invoices invoice
      WHERE invoice.tenant_id = wallet.tenant_id
        AND invoice.reference_type = 'crypto_payment'
        AND invoice.reference_id = wallet.tx_hash
  )
ORDER BY wallet.completed_at
LIMIT 100;

-- name: SetCryptoWalletCreditedAmount :execrows
UPDATE purser.crypto_wallets
SET credited_amount_cents = sqlc.arg(amount_cents),
    credited_amount_currency = sqlc.arg(currency), updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: ConfirmCryptoInvoicePayment :one
UPDATE purser.billing_payments payment
SET tx_id = sqlc.arg(tx_hash), status = 'confirmed', confirmed_at = sqlc.arg(confirmed_at),
    updated_at = NOW(), actual_tx_amount = sqlc.arg(actual_tx_amount),
    asset_type = sqlc.arg(asset_type), network = sqlc.arg(network), block_number = sqlc.arg(block_number)
FROM purser.billing_invoices invoice
WHERE payment.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND payment.invoice_id = invoice.id
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND payment.method = sqlc.arg(method)
  AND payment.status = 'pending'
  AND payment.tx_id = sqlc.arg(wallet_address)
RETURNING payment.id::text AS id, payment.amount::float8 AS amount,
          payment.currency;

-- name: MarkCryptoInvoicePaid :execrows
UPDATE purser.billing_invoices
SET status = 'paid', paid_at = sqlc.arg(paid_at), updated_at = NOW()
WHERE id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status IN ('pending', 'overdue');
