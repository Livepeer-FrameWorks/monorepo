ALTER TABLE navigator.tenant_edge_apply_state
    ADD COLUMN IF NOT EXISTS last_delivery_sequence BIGINT NOT NULL DEFAULT 0;

DO $migration$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'ck_navigator_tenant_edge_apply_delivery_sequence'
          AND conrelid = 'navigator.tenant_edge_apply_state'::regclass
    ) THEN
        ALTER TABLE navigator.tenant_edge_apply_state
            ADD CONSTRAINT ck_navigator_tenant_edge_apply_delivery_sequence
            CHECK (last_delivery_sequence >= 0) NOT VALID;
    END IF;
END
$migration$;
