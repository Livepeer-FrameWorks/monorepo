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

-- name: ProviderDocumentPredatesAssessedTax :one
-- A provider document issued while the tenant's relationship kept its untaxed
-- treatment. Provider timestamps have whole-second precision, so a document in
-- the same second as the move to assessed handling counts as assessed.
SELECT EXISTS (
    SELECT 1 FROM purser.tenant_subscriptions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND (tax_assessed_since IS NULL
           OR sqlc.arg(issued_at)::timestamptz < date_trunc('second', tax_assessed_since))
)::boolean AS predates_assessed_tax;
