-- name: TenantKeepsUnassessedTax :one
-- A billing relationship with no tax_assessed_since keeps its untaxed
-- treatment: no provider tax synchronization and no tax facts on its invoices.
SELECT EXISTS (
    SELECT 1 FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND status <> 'cancelled'
      AND tax_assessed_since IS NULL
)::boolean AS keeps_unassessed;

-- name: StartAssessedTaxHandling :execrows
UPDATE purser.tenant_subscriptions
SET tax_assessed_since = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status <> 'cancelled'
  AND tax_assessed_since IS NULL;
