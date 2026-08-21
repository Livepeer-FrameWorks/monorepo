-- name: GetX402PaymentQuote :one
SELECT id::text AS id, tenant_id::text AS tenant_id, resource, resource_class, network,
       asset, pay_to, amount_atomic::text AS amount_atomic, credit_amount_cents,
       eur_per_usd_rate::text AS eur_per_usd_rate, requirements_json,
       tax_document_kind, tax_profile_snapshot, expires_at, status
FROM purser.x402_payment_quotes
WHERE id = sqlc.arg(quote_id)::text::uuid
  AND tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: GetX402PrepaidBalanceCents :one
SELECT balance_cents
FROM purser.prepaid_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND currency = 'EUR';

-- name: ExpireOfferedX402PaymentQuote :exec
UPDATE purser.x402_payment_quotes
SET status = 'expired', updated_at = NOW()
WHERE id = sqlc.arg(quote_id)::text::uuid AND status = 'offered';

-- name: ClaimX402PaymentQuote :execrows
UPDATE purser.x402_payment_quotes
SET status = 'claiming', claim_token = sqlc.arg(claim_token)::text::uuid,
    claim_expires_at = NOW() + INTERVAL '2 minutes', updated_at = NOW()
WHERE id = sqlc.arg(quote_id)::text::uuid
  AND expires_at > NOW()
  AND (
      status = 'offered'
      OR (
          status = 'claiming'
          AND claim_expires_at < NOW()
          AND NOT EXISTS (
              SELECT 1 FROM purser.x402_nonces nonce WHERE nonce.quote_id = x402_payment_quotes.id
          )
      )
  );

-- name: CreateX402PaymentQuote :exec
INSERT INTO purser.x402_payment_quotes (
    id, tenant_id, resource, resource_class, network, asset, pay_to,
    amount_atomic, credit_amount_cents, credit_currency,
    eur_per_usd_rate, requirements_json, tax_document_kind,
    tax_profile_snapshot, expires_at
) VALUES (
    sqlc.arg(id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(resource), sqlc.arg(resource_class), sqlc.arg(network),
    sqlc.arg(asset), sqlc.arg(pay_to), sqlc.arg(amount_atomic)::text::numeric,
    sqlc.arg(credit_amount_cents), 'EUR', sqlc.arg(eur_per_usd_rate)::text::numeric,
    sqlc.arg(requirements_json), sqlc.arg(tax_document_kind),
    sqlc.arg(tax_profile_snapshot), sqlc.arg(expires_at)
);
