ALTER TABLE quartermaster.navigator_tenant_alias_outbox
    VALIDATE CONSTRAINT navigator_tenant_alias_outbox_action_check;

ALTER TABLE quartermaster.navigator_tenant_alias_outbox
    VALIDATE CONSTRAINT chk_alias_outbox_cluster;
