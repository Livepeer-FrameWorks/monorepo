-- name: EnsurePrepaidBalanceRow :exec
INSERT INTO purser.prepaid_balances (
    tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (sqlc.arg(tenant_id)::text::uuid, 0, sqlc.arg(currency), 500, NOW(), NOW())
ON CONFLICT (tenant_id, currency) DO NOTHING;

-- name: InsertReferencedBalanceTransaction :one
INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, actor_kind, actor_id, reason, evidence_ref, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_cents), 0,
    sqlc.arg(transaction_type), sqlc.arg(description),
    sqlc.arg(reference_id)::text::uuid, sqlc.arg(reference_type),
    sqlc.arg(actor_kind), sqlc.narg(actor_id)::text::uuid,
    sqlc.arg(reason), sqlc.narg(evidence_ref), sqlc.arg(created_at)
)
ON CONFLICT (tenant_id, reference_type, reference_id)
WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO NOTHING
RETURNING id;

-- name: GetBalanceTransactionByReference :one
SELECT id, tenant_id, amount_cents, balance_after_cents, transaction_type,
       COALESCE(description, '') AS description, reference_id, reference_type,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND reference_type = sqlc.arg(reference_type)
  AND reference_id = sqlc.arg(reference_id)::text::uuid
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: AddPrepaidBalance :one
UPDATE purser.prepaid_balances
SET balance_cents = balance_cents + sqlc.arg(amount_cents), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND currency = sqlc.arg(currency)
RETURNING balance_cents;

-- name: SetBalanceTransactionResult :execrows
UPDATE purser.balance_transactions
SET balance_after_cents = sqlc.arg(balance_after_cents)
WHERE id = sqlc.arg(id);

-- name: InsertBalanceTransaction :exec
INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, actor_kind, actor_id, reason, evidence_ref, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_cents),
    sqlc.arg(balance_after_cents), sqlc.arg(transaction_type), sqlc.arg(description),
    sqlc.narg(reference_id)::text::uuid, sqlc.narg(reference_type),
    sqlc.arg(actor_kind), sqlc.narg(actor_id)::text::uuid,
    sqlc.arg(reason), sqlc.narg(evidence_ref), sqlc.arg(created_at)
);

-- name: ReactivateFundedSubscription :execrows
UPDATE purser.tenant_subscriptions
SET status = 'active', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status = 'suspended';

-- name: ListBalanceTransactions :many
SELECT id, tenant_id, amount_cents, balance_after_cents, transaction_type,
       COALESCE(description, '') AS description, reference_id, reference_type,
       COALESCE(created_at, TIMESTAMPTZ 'epoch') AS created_at
FROM purser.balance_transactions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND (NOT sqlc.arg(filter_type)::boolean OR transaction_type = sqlc.arg(transaction_type))
  AND (NOT sqlc.arg(filter_start)::boolean OR created_at >= sqlc.arg(start_at))
  AND (NOT sqlc.arg(filter_end)::boolean OR created_at <= sqlc.arg(end_at))
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: InitializePrepaidBalanceRow :execrows
INSERT INTO purser.prepaid_balances (
    id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id)::text::uuid, sqlc.arg(balance_cents),
    sqlc.arg(currency), sqlc.arg(low_balance_threshold_cents), sqlc.arg(now), sqlc.arg(now)
)
ON CONFLICT (tenant_id, currency) DO NOTHING;
