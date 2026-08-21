-- name: ListAllocatedDepositsForCanonicalityCheck :many
SELECT id::text AS id, block_number, block_hash
FROM purser.crypto_deposit_events
WHERE network = sqlc.arg(network) AND canonical AND status = 'allocated'
ORDER BY last_canonical_checked_at ASC NULLS FIRST, block_number DESC
LIMIT sqlc.arg(row_limit);

-- name: TouchAllocatedDepositCanonicality :exec
UPDATE purser.crypto_deposit_events
SET last_canonical_checked_at = NOW()
WHERE id = sqlc.arg(event_id)::text::uuid
  AND canonical AND status = 'allocated'
  AND block_hash = sqlc.arg(block_hash);

-- name: LockAllocatedDepositReversal :one
SELECT event.status AS event_status, event.canonical, wallet.network,
       wallet.tenant_id::text AS tenant_id, wallet.purpose,
       COALESCE(wallet.invoice_id::text, '')::text AS invoice_id, wallet.credited_amount_cents,
       wallet.credited_amount_currency, wallet.id::text AS wallet_id, wallet.tx_hash
FROM purser.crypto_deposit_events event
JOIN purser.crypto_wallets wallet ON wallet.id = event.wallet_id
WHERE event.id = sqlc.arg(event_id)::text::uuid
FOR UPDATE OF event, wallet;

-- name: MarkAllocatedDepositReorged :execrows
UPDATE purser.crypto_deposit_events
SET canonical = FALSE, status = 'reorged', reorged_at = NOW(),
    last_canonical_checked_at = NOW(),
    allocation_error = 'allocated deposit block is no longer canonical'
WHERE id = sqlc.arg(event_id)::text::uuid AND canonical AND status = 'allocated';

-- name: MarkReorgedCryptoWalletForReview :exec
UPDATE purser.crypto_wallets
SET status = 'review_required', updated_at = NOW()
WHERE id = sqlc.arg(wallet_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: LockCryptoTopupBalanceTransaction :one
SELECT id::text AS id, amount_cents
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND reference_type = sqlc.arg(reference_type)
  AND reference_id = sqlc.arg(reference_id)::text::uuid
  AND transaction_type = 'topup'
FOR UPDATE;

-- name: CryptoReversalBalanceTransactionExists :one
SELECT EXISTS(
    SELECT 1 FROM purser.balance_transactions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND reference_type = sqlc.arg(reference_type)
      AND reference_id = sqlc.arg(reference_id)::text::uuid
);

-- name: InsertCryptoReversalBalanceTransaction :exec
INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type,
    description, reference_id, reference_type, actor_kind, reason,
    evidence_ref, reverses_transaction_id, created_at
) VALUES (
    sqlc.arg(id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(amount_cents), sqlc.arg(balance_after_cents), 'reversal',
    sqlc.arg(description), sqlc.arg(reference_id)::text::uuid,
    sqlc.arg(reference_type), 'job', sqlc.arg(reason),
    sqlc.arg(evidence_ref), sqlc.arg(reverses_transaction_id)::text::uuid, NOW()
);

-- name: InsertReorgedCryptoTopupCreditNote :exec
INSERT INTO purser.credit_notes (
    credit_note_number, tenant_id, source_document_type, source_document_id,
    reversal_reference_type, reversal_reference_id, amount_cents, currency,
    reason, evidence_json
)
SELECT 'CN-' || lpad(nextval('purser.credit_note_number_seq')::text, 10, '0'),
       invoice.tenant_id, invoice.source_type, invoice.id,
       'crypto_reorg', sqlc.arg(event_id)::text, invoice.gross_amount_cents, invoice.currency,
       'confirmed direct crypto top-up reversed after canonicality failure',
       jsonb_build_object('transaction_hash', sqlc.arg(tx_hash)::text, 'event_id', sqlc.arg(event_id)::text)
FROM (
    SELECT id, tenant_id, reference_type, reference_id, gross_amount_cents, currency,
           'simplified_invoice'::text AS source_type
    FROM purser.simplified_invoices
    UNION ALL
    SELECT id, tenant_id, reference_type, reference_id, gross_amount_cents, currency,
           'crypto_invoice'::text AS source_type
    FROM purser.crypto_invoices
) invoice
WHERE invoice.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND invoice.reference_type = 'crypto_payment'
  AND invoice.reference_id = sqlc.arg(tx_hash)::text
ON CONFLICT (source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
DO NOTHING;

-- name: LockConfirmedCryptoInvoicePayment :one
SELECT id::text AS id, (amount * 100)::bigint AS amount_cents, currency
FROM purser.billing_payments
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tx_id = sqlc.arg(tx_hash)
  AND method IN ('crypto_eth', 'crypto_usdc') AND status = 'confirmed'
FOR UPDATE;

-- name: InsertCryptoReorgPaymentReversal :one
INSERT INTO purser.payment_reversals (
    tenant_id, payment_id, invoice_id, provider, reversal_type,
    provider_reversal_id, amount_cents, currency, status, reason,
    operator_review_required, actor_kind, evidence_ref
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(payment_id)::text::uuid,
    sqlc.arg(invoice_id)::text::uuid, 'manual', 'manual',
    sqlc.arg(provider_reversal_id), sqlc.arg(amount_cents), sqlc.arg(currency),
    'succeeded', sqlc.arg(reason), TRUE, 'job', sqlc.arg(evidence_ref)
)
ON CONFLICT (provider, provider_reversal_id) DO NOTHING
RETURNING id::text AS id;

-- name: InsertReorgedCryptoInvoiceCreditNote :exec
INSERT INTO purser.credit_notes (
    credit_note_number, tenant_id, source_document_type, source_document_id,
    reversal_reference_type, reversal_reference_id, amount_cents, currency,
    reason, evidence_json
) VALUES (
    'CN-' || lpad(nextval('purser.credit_note_number_seq')::text, 10, '0'),
    sqlc.arg(tenant_id)::text::uuid, 'invoice', sqlc.arg(invoice_id)::text::uuid,
    'crypto_reorg', sqlc.arg(event_id), sqlc.arg(amount_cents), sqlc.arg(currency),
    'confirmed crypto invoice payment reversed after canonicality failure',
    jsonb_build_object('payment_id', sqlc.arg(payment_id)::text, 'transaction_hash', sqlc.arg(tx_hash)::text)
)
ON CONFLICT (source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
DO NOTHING;
