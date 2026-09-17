-- v0.3.5: platform-scoped service events (an ownerless cluster has no tenant)
-- and lease-token fencing for outbox settlement.

ALTER TABLE quartermaster.service_event_outbox
    ALTER COLUMN tenant_id DROP NOT NULL;

ALTER TABLE quartermaster.service_event_outbox
    ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'tenant',
    ADD COLUMN IF NOT EXISTS lease_token UUID;

ALTER TABLE quartermaster.service_event_outbox
    DROP CONSTRAINT IF EXISTS chk_qm_service_event_outbox_scope;

ALTER TABLE quartermaster.service_event_outbox
    DROP CONSTRAINT IF EXISTS chk_qm_service_event_outbox_scope_tenant;

ALTER TABLE quartermaster.service_event_outbox
    ADD CONSTRAINT chk_qm_service_event_outbox_scope
        CHECK (scope IN ('tenant', 'platform')) NOT VALID,
    ADD CONSTRAINT chk_qm_service_event_outbox_scope_tenant
        CHECK (scope = 'platform' OR tenant_id IS NOT NULL) NOT VALID;
