ALTER TABLE foghorn.ingest_offline_effects
    ADD COLUMN IF NOT EXISTS drain_publisher BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS drain_done BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS drain_runtime_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS state_superseded BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE foghorn.ingest_offline_effects
    DROP CONSTRAINT IF EXISTS ck_foghorn_ingest_offline_effects_drain_binding;
ALTER TABLE foghorn.ingest_offline_effects
    DROP CONSTRAINT IF EXISTS ck_foghorn_ingest_offline_effects_drain_settled;
ALTER TABLE foghorn.ingest_offline_effects
    ADD CONSTRAINT ck_foghorn_ingest_offline_effects_drain_binding
        CHECK (NOT drain_publisher OR (source_generation IS NOT NULL AND drain_runtime_name <> '')) NOT VALID,
    ADD CONSTRAINT ck_foghorn_ingest_offline_effects_drain_settled
        CHECK (state NOT IN ('applied', 'superseded') OR NOT drain_publisher OR drain_done) NOT VALID;
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_drain_ack
    ON foghorn.ingest_offline_effects(source_node_id, source_generation)
    WHERE drain_publisher AND NOT drain_done AND state IN ('pending', 'failed');
