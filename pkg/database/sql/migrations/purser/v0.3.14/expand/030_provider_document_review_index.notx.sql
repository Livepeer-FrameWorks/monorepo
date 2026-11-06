CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_provider_document_tax_review_due
ON purser.provider_billing_documents(tenant_id,COALESCE(tax_reviewed_at,captured_at),id)
WHERE COALESCE(reviewed_tax_status,tax_status)='legacy_unknown';
