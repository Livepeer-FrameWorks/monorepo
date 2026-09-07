UPDATE foghorn.ingest_admission_effects
SET target_revision = 0
WHERE target_revision < 0;

UPDATE foghorn.ingest_offline_effects
SET state = 'failed',
    last_error = COALESCE(last_error, 'normalized unsupported legacy state'),
    applied_at = COALESCE(applied_at, NOW()),
    updated_at = NOW()
WHERE state NOT IN ('pending', 'applied', 'superseded', 'failed');

UPDATE foghorn.push_target_status_outbox
SET status = 'failed', reason_code = 'configuration_error',
    last_error = COALESCE(last_error, 'normalized unsupported legacy status'),
    updated_at = NOW()
WHERE status NOT IN ('pending', 'pushing', 'retrying', 'stopping', 'idle', 'failed');

UPDATE foghorn.push_target_status_outbox
SET reason_code = 'unspecified', updated_at = NOW()
WHERE reason_code NOT IN ('unspecified', 'connected', 'completed', 'destination_rejected', 'network_error', 'process_error', 'capacity_exhausted', 'configuration_error', 'edge_upgrade_required', 'stopped');

ALTER TABLE foghorn.ingest_admission_effects
    VALIDATE CONSTRAINT ck_foghorn_ingest_admission_target_revision;
ALTER TABLE foghorn.admission_push_target_revisions
    VALIDATE CONSTRAINT ck_foghorn_admission_push_target_revision_positive;
ALTER TABLE foghorn.push_target_status_outbox
    VALIDATE CONSTRAINT ck_foghorn_push_target_status_outbox_status;
ALTER TABLE foghorn.push_target_status_outbox
    VALIDATE CONSTRAINT ck_foghorn_push_target_status_outbox_reason;

ALTER TABLE foghorn.ingest_offline_effects
    VALIDATE CONSTRAINT ck_foghorn_ingest_offline_effects_state;
