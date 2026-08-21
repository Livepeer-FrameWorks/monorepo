-- name: EnsureBillingCollectionBalance :exec
INSERT INTO purser.billing_collection_balances (tenant_id, currency, balance_cents)
VALUES (sqlc.arg(tenant_id)::text::uuid, sqlc.arg(currency), 0)
ON CONFLICT (tenant_id, currency) DO NOTHING;

-- name: LockBillingCollectionBalance :one
SELECT balance_cents
FROM purser.billing_collection_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency)
FOR UPDATE;

-- name: UpdateBillingCollectionBalance :execrows
UPDATE purser.billing_collection_balances
SET balance_cents = sqlc.arg(balance_cents), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency);

-- name: InsertBillingCollectionDecision :exec
INSERT INTO purser.billing_collection_entries (
    invoice_id, tenant_id, provider, currency, minimum_cents,
    opening_balance_cents, current_charge_cents, collected_cents,
    closing_balance_cents, outcome
) VALUES (
    sqlc.arg(invoice_id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(provider), sqlc.arg(currency), sqlc.arg(minimum_cents),
    sqlc.arg(opening_balance_cents), sqlc.arg(current_charge_cents),
    sqlc.arg(collected_cents), sqlc.arg(closing_balance_cents), sqlc.arg(outcome)
);

-- name: InsertBillingCollectionLineItem :exec
INSERT INTO purser.invoice_line_items (
    invoice_id, tenant_id, line_key, unit, dimensions, description,
    quantity, included_quantity, billable_quantity, unit_price, amount,
    currency, pricing_source
) VALUES (
    sqlc.arg(invoice_id)::text::uuid, sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(line_key), 'balance', sqlc.arg(dimensions)::jsonb,
    sqlc.arg(description), 1, 0, 1, sqlc.arg(amount)::text::numeric,
    sqlc.arg(amount)::text::numeric, sqlc.arg(currency), 'tier'
);
