DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'ck_navigator_tenant_edge_apply_state_state'
          AND conrelid = 'navigator.tenant_edge_apply_state'::regclass
    ) THEN
        ALTER TABLE navigator.tenant_edge_apply_state
            ADD CONSTRAINT ck_navigator_tenant_edge_apply_state_state
            CHECK (state IN ('pending_distribute', 'pending_apply', 'applied', 'in_dns')) NOT VALID;
    END IF;
END
$migration$;
