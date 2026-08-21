-- name: GetTenantBillingDetails :one
SELECT billing_email, billing_name, billing_company, tax_id,
       COALESCE(billing_address, '{}'::jsonb) AS billing_address,
       COALESCE(updated_at, TIMESTAMP 'epoch') AS updated_at
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateTenantBillingDetails :execrows
UPDATE purser.tenant_subscriptions
SET billing_email = CASE WHEN sqlc.arg(set_email)::boolean THEN sqlc.arg(email)::text ELSE billing_email END,
    billing_name = CASE WHEN sqlc.arg(set_name)::boolean THEN sqlc.arg(name)::text ELSE billing_name END,
    billing_company = CASE WHEN sqlc.arg(set_company)::boolean THEN sqlc.arg(company)::text ELSE billing_company END,
    tax_id = CASE WHEN sqlc.arg(set_vat_number)::boolean THEN sqlc.arg(vat_number)::text ELSE tax_id END,
    billing_address = CASE WHEN sqlc.arg(set_address)::boolean THEN sqlc.arg(address)::jsonb ELSE billing_address END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid AND status != 'cancelled';
