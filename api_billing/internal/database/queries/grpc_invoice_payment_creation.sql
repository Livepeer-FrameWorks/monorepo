-- name: LockInvoicePaymentCreation :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(invoice_id)::text, 0));

-- name: GetPayableInvoiceBalance :one
SELECT invoice.tenant_id::text AS tenant_id,
       invoice.amount::text AS total_amount,
       invoice.currency,
       COALESCE((
           SELECT SUM(payment.amount - (COALESCE(payment.reversed_amount_cents, 0)::numeric / 100))
           FROM purser.billing_payments payment
           WHERE payment.invoice_id = invoice.id
             AND payment.status = 'confirmed'
             AND payment.currency = invoice.currency
       ), 0)::text AS net_paid
FROM purser.billing_invoices invoice
WHERE invoice.id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.status IN ('pending', 'overdue')
FOR UPDATE;

-- name: GetActiveInvoicePayment :one
SELECT payment.id::text AS id, payment.method, payment.amount::text AS amount,
       payment.currency, payment.tx_id, payment.payment_url,
       COALESCE(payment.created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COUNT(*) OVER ()::int AS active_count
FROM purser.billing_payments payment
JOIN purser.billing_invoices invoice ON invoice.id = payment.invoice_id
WHERE payment.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND payment.status = 'pending'
ORDER BY payment.created_at DESC
LIMIT 1;

-- name: CreatePendingInvoicePayment :exec
INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at
) VALUES (
    sqlc.arg(payment_id)::text::uuid,
    sqlc.arg(invoice_id)::text::uuid,
    sqlc.arg(method),
    sqlc.arg(amount)::text::numeric,
    sqlc.arg(currency),
    NULLIF(sqlc.arg(tx_id)::text, ''),
    'pending',
    sqlc.arg(created_at),
    sqlc.arg(created_at)
);

-- name: GetCryptoScannerReadiness :one
SELECT scanned_at, last_error, lag_blocks
FROM purser.crypto_scan_cursors
WHERE network = sqlc.arg(network);

-- name: AttachCardCheckoutToPendingPayment :execrows
UPDATE purser.billing_payments
SET tx_id = sqlc.arg(provider_id), payment_url = sqlc.arg(payment_url), updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid AND status = 'pending';

-- name: MarkPendingInvoicePaymentFailed :execrows
UPDATE purser.billing_payments
SET status = 'failed', updated_at = NOW()
WHERE id = sqlc.arg(payment_id)::text::uuid AND status = 'pending';

-- name: ExpireStaleCardInvoicePayments :exec
UPDATE purser.billing_payments payment
SET status = 'failed', updated_at = NOW()
FROM purser.billing_invoices invoice
WHERE payment.invoice_id = invoice.id
  AND payment.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND payment.method = 'card'
  AND payment.status = 'pending'
  AND payment.created_at <= NOW() - INTERVAL '24 hours';

-- name: FailExpiredCryptoInvoicePayments :exec
UPDATE purser.billing_payments payment
SET status = 'failed', updated_at = NOW()
FROM purser.crypto_wallets wallet
WHERE payment.invoice_id = wallet.invoice_id
  AND payment.tx_id = wallet.wallet_address
  AND payment.method = sqlc.arg(method)
  AND payment.status = 'pending'
  AND wallet.invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND wallet.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND wallet.asset = sqlc.arg(asset)
  AND wallet.purpose = 'invoice'
  AND wallet.status IN ('pending', 'confirming')
  AND wallet.expires_at <= NOW();

-- name: ExpireCryptoInvoiceWallets :exec
UPDATE purser.crypto_wallets
SET status = 'expired', updated_at = NOW()
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND asset = sqlc.arg(asset)
  AND purpose = 'invoice'
  AND status IN ('pending', 'confirming')
  AND expires_at <= NOW();

-- name: GetActiveInvoiceCryptoPaymentQuote :one
SELECT wallet_address, expected_amount_base_units::text AS expected_amount_base_units,
       quoted_price_usd::text AS quoted_price_usd, quote_source, asset, network,
       quoted_at, expires_at
FROM purser.crypto_wallets
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND asset = sqlc.arg(asset)
  AND purpose = 'invoice'
  AND status IN ('pending', 'confirming')
  AND expires_at > NOW()
ORDER BY created_at DESC
LIMIT 1;
