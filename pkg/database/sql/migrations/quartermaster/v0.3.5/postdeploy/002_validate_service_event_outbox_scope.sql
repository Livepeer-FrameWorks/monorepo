ALTER TABLE quartermaster.service_event_outbox
    VALIDATE CONSTRAINT chk_qm_service_event_outbox_scope;

ALTER TABLE quartermaster.service_event_outbox
    VALIDATE CONSTRAINT chk_qm_service_event_outbox_scope_tenant;
