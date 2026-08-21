-- name: ReactivateSuspendedTenant :execrows
UPDATE purser.tenant_subscriptions
SET status = 'active', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status = 'suspended';

-- name: ClaimX402Mutation :execrows
INSERT INTO purser.x402_mutation_results (
    tenant_id, quote_id, idempotency_key, request_fingerprint, protocol, operation
)
SELECT sqlc.arg(tenant_id)::text::uuid, quote.id, sqlc.arg(idempotency_key),
       sqlc.arg(request_fingerprint), sqlc.arg(protocol), sqlc.arg(operation)
FROM purser.x402_payment_quotes quote
WHERE quote.id = sqlc.arg(quote_id)::text::uuid
  AND quote.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND quote.status = 'confirmed'
ON CONFLICT DO NOTHING;

-- name: GetX402MutationClaim :one
SELECT quote_id::text AS quote_id, request_fingerprint, protocol, operation, status,
       result, content_type, status_code, updated_at
FROM purser.x402_mutation_results
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: MarkX402MutationOperatorReview :exec
UPDATE purser.x402_mutation_results
SET status = 'operator_review',
    review_reason = 'mutation owner result was not durably recorded before the review deadline',
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key)
  AND status IN ('claimed', 'completion_pending');

-- name: CompleteX402Mutation :execrows
UPDATE purser.x402_mutation_results
SET status = 'completed', result = sqlc.arg(result)::bytea,
    content_type = NULLIF(sqlc.arg(content_type)::text, ''),
    status_code = sqlc.arg(status_code)::integer, completed_at = NOW(), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND quote_id = sqlc.arg(quote_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key)
  AND request_fingerprint = sqlc.arg(request_fingerprint)
  AND status IN ('claimed', 'completion_pending', 'operator_review');

-- name: IsX402MutationCompleted :one
SELECT status = 'completed' AS completed
FROM purser.x402_mutation_results
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND quote_id = sqlc.arg(quote_id)::text::uuid
  AND idempotency_key = sqlc.arg(idempotency_key)
  AND request_fingerprint = sqlc.arg(request_fingerprint);

-- name: ListInvoiceLineItemsForTenant :many
SELECT line_key, COALESCE(meter, '')::text AS meter, unit,
       COALESCE(dimensions, '{}'::jsonb) AS dimensions, description,
       quantity::text AS quantity, included_quantity::text AS included_quantity,
       billable_quantity::text AS billable_quantity, unit_price::text AS unit_price,
       amount::text AS amount, currency, COALESCE(cluster_id, '')::text AS cluster_id,
       COALESCE(cluster_kind, '')::text AS cluster_kind, pricing_source
FROM purser.invoice_line_items
WHERE invoice_id = sqlc.arg(invoice_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid
ORDER BY (line_key = 'base_subscription') DESC, line_key ASC;
