ALTER TABLE quartermaster.navigator_tenant_alias_outbox
    DROP CONSTRAINT IF EXISTS navigator_tenant_alias_outbox_action_check;

ALTER TABLE quartermaster.navigator_tenant_alias_outbox
    DROP CONSTRAINT IF EXISTS chk_alias_outbox_cluster;

ALTER TABLE quartermaster.navigator_tenant_alias_outbox
    ADD CONSTRAINT navigator_tenant_alias_outbox_action_check
        CHECK (action IN ('ensure', 'ensure_cluster', 'retire', 'remove', 'remove_cluster')) NOT VALID,
    ADD CONSTRAINT chk_alias_outbox_cluster
        CHECK (action NOT IN ('ensure_cluster', 'remove_cluster') OR NULLIF(btrim(cluster_id), '') IS NOT NULL) NOT VALID;
