DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'fk_navigator_tenant_edge_apply_alias'
          AND conrelid = 'navigator.tenant_edge_apply_state'::regclass
    ) THEN
        ALTER TABLE navigator.tenant_edge_apply_state
            ADD CONSTRAINT fk_navigator_tenant_edge_apply_alias
            FOREIGN KEY (tenant_id)
            REFERENCES navigator.tenant_aliases(tenant_id)
            ON DELETE CASCADE
            NOT VALID;
    END IF;
END
$migration$;
