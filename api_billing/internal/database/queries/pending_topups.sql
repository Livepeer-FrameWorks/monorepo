-- name: GetPendingTopupByID :one
SELECT id, tenant_id, provider, COALESCE(checkout_id, '')::text AS checkout_id,
       amount_cents, currency, status, expires_at, completed_at,
       balance_transaction_id,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMPTZ 'epoch') AS updated_at
FROM purser.pending_topups
WHERE id = sqlc.arg(topup_id)::text::uuid;

-- name: GetPendingTopupByCheckout :one
SELECT id, tenant_id, provider, COALESCE(checkout_id, '')::text AS checkout_id,
       amount_cents, currency, status, expires_at, completed_at,
       balance_transaction_id,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMPTZ 'epoch') AS updated_at
FROM purser.pending_topups
WHERE provider = sqlc.arg(provider) AND checkout_id = sqlc.arg(checkout_id);

-- name: ListPendingTopups :many
SELECT id, tenant_id, provider, COALESCE(checkout_id, '')::text AS checkout_id,
       amount_cents, currency, status, expires_at, completed_at,
       balance_transaction_id,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       COALESCE(updated_at, TIMESTAMPTZ 'epoch') AS updated_at
FROM purser.pending_topups
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_status)::boolean OR status = sqlc.arg(status))
ORDER BY created_at DESC, id DESC
LIMIT 50;

-- name: UpsertPrepaidTopupProviderIntent :one
INSERT INTO purser.payment_provider_intents (
    tenant_id, provider, purpose, local_reference_type, local_reference_id,
    status, currency, amount_cents, idempotency_key, expires_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(provider), 'prepaid_topup',
    'pending_topups', sqlc.arg(topup_id)::text::uuid, 'pending',
    sqlc.arg(currency), sqlc.arg(amount_cents), sqlc.arg(idempotency_key),
    sqlc.arg(expires_at)
)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET
    attempt_count = purser.payment_provider_intents.attempt_count + 1,
    updated_at = NOW()
RETURNING id::text AS id;

-- name: InsertPendingCardTopup :exec
INSERT INTO purser.pending_topups (
    id, tenant_id, provider, checkout_id, amount_cents, currency,
    status, expires_at, billing_email, billing_name, billing_company,
    billing_vat_number, intent_id
) VALUES (
    sqlc.arg(topup_id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(provider), NULL, sqlc.arg(amount_cents), sqlc.arg(currency),
    'pending', sqlc.arg(expires_at), sqlc.narg(billing_email),
    sqlc.narg(billing_name), sqlc.narg(billing_company),
    sqlc.narg(billing_vat_number), sqlc.arg(intent_id)::text::uuid
);

-- name: FailPendingCardTopup :execrows
UPDATE purser.pending_topups
SET status = 'failed', updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid AND status = 'pending';

-- name: OpenPrepaidTopupProviderIntent :exec
UPDATE purser.payment_provider_intents
SET provider_session_id = sqlc.arg(session_id), expires_at = sqlc.arg(expires_at),
    status = 'provider_open', updated_at = NOW()
WHERE id = sqlc.arg(intent_id)::text::uuid;

-- name: AttachCheckoutToPendingTopup :execrows
UPDATE purser.pending_topups
SET checkout_id = sqlc.arg(session_id), expires_at = sqlc.arg(expires_at), updated_at = NOW()
WHERE id = sqlc.arg(topup_id)::text::uuid AND status = 'pending';

-- name: GetPrepaidCryptoTopup :one
SELECT id::text AS id, tenant_id::text AS tenant_id, wallet_address, asset,
       expected_amount_cents, status, tx_hash, COALESCE(confirmations, 0)::int AS confirmations,
       COALESCE(received_amount_base_units::text, '')::text AS received_amount_base_units,
       credited_amount_cents, expires_at, detected_at, completed_at,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at,
       credited_amount_currency, quote_source, COALESCE(network, '')::text AS network
FROM purser.crypto_wallets
WHERE id = sqlc.arg(topup_id)::text::uuid
  AND purpose = 'prepaid'
  AND (NOT sqlc.arg(enforce_tenant)::boolean OR tenant_id = sqlc.arg(tenant_id)::text::uuid);
