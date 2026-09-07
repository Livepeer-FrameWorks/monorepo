ALTER TABLE foghorn.ingest_admission_effects
    ADD COLUMN IF NOT EXISTS target_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS capacity_pending BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS foghorn.admission_push_target_revisions (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    node_id VARCHAR(100) NOT NULL,
    source_generation UUID NOT NULL,
    target_revision BIGINT NOT NULL,
    activation_attempt UUID NOT NULL DEFAULT gen_random_uuid(),
    push_targets BYTEA NOT NULL,
    mist_push_ids JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_foghorn_admission_push_target_revision UNIQUE (source_generation, target_revision)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'foghorn.admission_push_target_revisions'::regclass
          AND conname = 'ck_foghorn_admission_push_target_revision_positive'
    ) THEN
        ALTER TABLE foghorn.admission_push_target_revisions
            ADD CONSTRAINT ck_foghorn_admission_push_target_revision_positive
            CHECK (target_revision > 0) NOT VALID;
    END IF;
END;
$$;

CREATE INDEX IF NOT EXISTS idx_foghorn_admission_push_target_revisions_lookup
    ON foghorn.admission_push_target_revisions(tenant_id, source_generation, target_revision);
CREATE INDEX IF NOT EXISTS idx_foghorn_admission_push_target_revisions_created
    ON foghorn.admission_push_target_revisions(created_at, id);

ALTER TABLE foghorn.admission_push_target_revisions
    ADD COLUMN IF NOT EXISTS mist_push_ids JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS activation_attempt UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'::uuid;

-- The release is still pre-contract: existing rows were written by a binary
-- that embedded the UUID attempt in the encrypted payload. They cannot be
-- recovered in SQL, so leave them non-bindable until the owning live
-- obligation is authoritatively re-armed by Foghorn.

ALTER TABLE foghorn.ingest_offline_effects
    ADD COLUMN IF NOT EXISTS teardown_done BOOLEAN NOT NULL DEFAULT TRUE;

-- Rows that predate the per-leg acknowledgement already ran under the legacy
-- all-or-nothing worker, so the add-column default preserves them as settled.
-- New rows inserted by an old binary during a rolling upgrade must default
-- FALSE so an upgraded worker cannot skip their teardown obligation.

ALTER TABLE foghorn.ingest_offline_effects
    ALTER COLUMN teardown_done SET DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS set_node_offline_done BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS broadcast_offline_done BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS decklog_done BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS ack_wait_attempts INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_teardown_ack
    ON foghorn.ingest_offline_effects(source_node_id, source_generation)
    WHERE teardown_stream AND NOT teardown_done AND state IN ('pending', 'failed');

ALTER TABLE foghorn.ingest_offline_effects
    DROP CONSTRAINT IF EXISTS ingest_offline_effects_state_check;
ALTER TABLE foghorn.ingest_offline_effects
    DROP CONSTRAINT IF EXISTS ck_foghorn_ingest_offline_effects_state;
ALTER TABLE foghorn.ingest_offline_effects
    ADD CONSTRAINT ck_foghorn_ingest_offline_effects_state
    CHECK (state IN ('pending', 'applied', 'superseded', 'failed')) NOT VALID;

ALTER TABLE foghorn.ingest_admission_effects
    DROP CONSTRAINT IF EXISTS ck_foghorn_ingest_admission_target_revision;
ALTER TABLE foghorn.ingest_admission_effects
    ADD CONSTRAINT ck_foghorn_ingest_admission_target_revision
    CHECK (target_revision >= 0) NOT VALID;

ALTER TABLE foghorn.push_target_status_outbox
    ADD COLUMN IF NOT EXISTS reason_code VARCHAR(32) NOT NULL DEFAULT 'unspecified';

ALTER TABLE foghorn.push_target_status_outbox
    DROP CONSTRAINT IF EXISTS ck_foghorn_push_target_status_outbox_status;
ALTER TABLE foghorn.push_target_status_outbox
    ADD CONSTRAINT ck_foghorn_push_target_status_outbox_status
    CHECK (status IN ('pending', 'pushing', 'retrying', 'stopping', 'idle', 'failed')) NOT VALID;

ALTER TABLE foghorn.push_target_status_outbox
    DROP CONSTRAINT IF EXISTS ck_foghorn_push_target_status_outbox_reason;
ALTER TABLE foghorn.push_target_status_outbox
    ADD CONSTRAINT ck_foghorn_push_target_status_outbox_reason
    CHECK (reason_code IN ('unspecified', 'connected', 'completed', 'destination_rejected', 'network_error', 'process_error', 'capacity_exhausted', 'configuration_error', 'edge_upgrade_required', 'stopped')) NOT VALID;
