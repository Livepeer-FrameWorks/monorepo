DROP INDEX IF EXISTS foghorn.idx_foghorn_ingest_offline_effects_terminal;
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_offline_effects_terminal
    ON foghorn.ingest_offline_effects(updated_at)
    WHERE state IN ('applied', 'superseded', 'failed');
