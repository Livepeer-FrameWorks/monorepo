-- name: FinancialAuditPage :many
WITH records AS (
    SELECT 'invoices'::text AS source, i.id::text AS id,
           COALESCE(i.finalized_at, i.created_at) AS occurred_at, to_jsonb(i)::text AS payload
    FROM purser.billing_invoices i WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'payments', p.id::text, p.created_at,
           (to_jsonb(p) || jsonb_build_object('tenant_id', i.tenant_id))::text
    FROM purser.billing_payments p
    JOIN purser.billing_invoices i ON i.id = p.invoice_id
    WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'simplified_invoices', i.id::text, i.issued_at, to_jsonb(i)::text
    FROM purser.simplified_invoices i WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'crypto_invoices', i.id::text, i.issued_at, to_jsonb(i)::text
    FROM purser.crypto_invoices i WHERE i.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'credit_notes', n.id::text, n.issued_at, to_jsonb(n)::text
    FROM purser.credit_notes n WHERE n.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'provider_settlements', p.id::text, p.created_at, to_jsonb(p)::text
    FROM purser.provider_settlements p WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'pending_topups', p.id::text, p.created_at, to_jsonb(p)::text
    FROM purser.pending_topups p WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid
    UNION ALL
    SELECT 'balance_transactions', p.id::text, p.created_at, to_jsonb(p)::text
    FROM purser.balance_transactions p WHERE p.tenant_id = sqlc.arg(tenant_id)::text::uuid
)
SELECT id, payload
FROM records
WHERE source = sqlc.arg(source)::text
  AND (occurred_at IS NULL OR (
      occurred_at >= sqlc.arg(from_at)::timestamptz
      AND occurred_at < sqlc.arg(through_at)::timestamptz
  ))
  AND id > sqlc.arg(after_id)::text
ORDER BY id
LIMIT 101;
