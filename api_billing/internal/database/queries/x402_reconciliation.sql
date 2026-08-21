-- name: ListSubmittingX402Intents :many
SELECT id::text AS id, network, payer_address, nonce,
       tenant_id::text AS tenant_id, amount_cents, settled_at,
       COALESCE(auth_payload::text, '')::text AS auth_payload,
       COALESCE(tx_hash, '')::text AS tx_hash
FROM purser.x402_nonces
WHERE status = 'submitting'
  AND settled_at < NOW() - INTERVAL '30 seconds'
ORDER BY settled_at ASC
LIMIT 50;

-- name: AdvanceConsumedX402Attempt :exec
WITH advanced AS (
    UPDATE purser.x402_nonces
    SET status = 'pending', submitted_at = COALESCE(submitted_at, NOW())
    WHERE id = sqlc.arg(id)::text::uuid AND status = 'submitting'
    RETURNING id
)
UPDATE purser.x402_settlement_attempts
SET state = 'broadcast', updated_at = NOW()
WHERE settlement_id IN (SELECT id FROM advanced)
  AND transaction_hash = LOWER(sqlc.arg(tx_hash));

-- name: ClaimSubmittingX402Intent :execrows
UPDATE purser.x402_nonces
SET last_submit_attempt_at = NOW()
WHERE id = sqlc.arg(id)::text::uuid
  AND status = 'submitting'
  AND (last_submit_attempt_at IS NULL OR last_submit_attempt_at < NOW() - INTERVAL '2 minutes');

-- name: ListPendingX402Settlements :many
SELECT id::text AS id, network, tx_hash, tenant_id::text AS tenant_id,
       amount_cents, settled_at, COALESCE(client_ip::text, '')::text AS client_ip,
       COALESCE(auth_payload::text, '')::text AS auth_payload
FROM purser.x402_nonces
WHERE status = 'pending'
  AND settled_at < NOW() - INTERVAL '15 seconds'
ORDER BY settled_at ASC
LIMIT 50;

-- name: ListRecoverableFailedX402Settlements :many
SELECT id::text AS id, network, tx_hash, tenant_id::text AS tenant_id,
       amount_cents, settled_at, COALESCE(auth_payload::text, '')::text AS auth_payload
FROM purser.x402_nonces
WHERE status = 'failed'
  AND (failure_reason LIKE 'timeout%' OR failure_reason = 'transaction reorged or missing')
  AND settled_at > NOW() - (sqlc.arg(recovery_window_hours) * INTERVAL '1 hour')
ORDER BY settled_at ASC
LIMIT 50;

-- name: ListConfirmedX402SettlementsForReconciliation :many
SELECT nonce.id::text AS id, nonce.network, nonce.tx_hash,
       nonce.tenant_id::text AS tenant_id, nonce.amount_cents, nonce.settled_at,
       nonce.block_number, COALESCE(nonce.client_ip::text, '')::text AS client_ip
FROM purser.x402_nonces nonce
WHERE nonce.status = 'confirmed'
  AND (
      nonce.confirmed_at > NOW() - INTERVAL '1 hour'
      OR nonce.rollup_applied_at IS NULL
      OR NOT EXISTS (
          SELECT 1 FROM (
              SELECT tenant_id, reference_type, reference_id FROM purser.simplified_invoices
              UNION ALL
              SELECT tenant_id, reference_type, reference_id FROM purser.crypto_invoices
          ) invoice
          WHERE invoice.tenant_id = nonce.tenant_id
            AND invoice.reference_type = 'x402_payment'
            AND invoice.reference_id = nonce.tx_hash
      )
  )
ORDER BY nonce.confirmed_at ASC
LIMIT 50;

-- name: UpdatePendingX402Receipt :exec
UPDATE purser.x402_nonces
SET block_number = sqlc.arg(block_number), gas_used = sqlc.arg(gas_used)
WHERE id = sqlc.arg(id)::text::uuid;

-- name: MarkX402SettlementConfirmed :exec
UPDATE purser.x402_nonces
SET status = 'confirmed', confirmed_at = NOW(),
    block_number = sqlc.arg(block_number), gas_used = sqlc.arg(gas_used)
WHERE id = sqlc.arg(id)::text::uuid;

-- name: MarkX402SettlementFailed :exec
UPDATE purser.x402_nonces
SET status = 'failed', failure_reason = sqlc.arg(reason)
WHERE id = sqlc.arg(id)::text::uuid;

-- name: InsertX402ReversalCreditNote :exec
INSERT INTO purser.credit_notes (
    credit_note_number, tenant_id, source_document_type, source_document_id,
    reversal_reference_type, reversal_reference_id, amount_cents, currency,
    reason, evidence_json
)
SELECT 'CN-' || lpad(nextval('purser.credit_note_number_seq')::text, 10, '0'),
       document.tenant_id, document.source_type, document.id, 'x402_failed', sqlc.arg(nonce_id),
       document.gross_amount_cents, document.currency, 'x402 settlement reversed after confirmation',
       jsonb_build_object('transaction_hash', sqlc.arg(tx_hash)::text, 'original_invoice_number', document.invoice_number)
FROM (
    SELECT id, tenant_id, reference_type, reference_id, gross_amount_cents, currency,
           invoice_number, 'simplified_invoice'::text AS source_type
    FROM purser.simplified_invoices
    UNION ALL
    SELECT id, tenant_id, reference_type, reference_id, gross_amount_cents, currency,
           invoice_number, 'crypto_invoice'::text AS source_type
    FROM purser.crypto_invoices
) document
WHERE document.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND document.reference_type = 'x402_payment'
  AND LOWER(document.reference_id) = LOWER(sqlc.arg(tx_hash))
ON CONFLICT (source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
DO NOTHING;
