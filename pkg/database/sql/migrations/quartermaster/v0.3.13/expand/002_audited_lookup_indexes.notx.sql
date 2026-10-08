CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_qm_navigator_tenant_alias_outbox_seq
    ON quartermaster.navigator_tenant_alias_outbox(seq ASC)
    WHERE completed_at IS NULL;
