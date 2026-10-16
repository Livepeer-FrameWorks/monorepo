CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_navigator_tenant_alias_outbox_completed_range
    ON quartermaster.navigator_tenant_alias_outbox(completed_at ASC)
    WHERE completed_at IS NOT NULL;
