CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenant_account_states_closure_failures
ON commodore.tenant_account_states(closure_failures ASC, tenant_id ASC)
WHERE quartermaster_closed_at IS NULL AND closure_error IS NOT NULL;
