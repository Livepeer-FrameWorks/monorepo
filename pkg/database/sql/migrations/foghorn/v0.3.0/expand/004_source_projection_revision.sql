-- This is the legacy-sequence bridge that runs before the key-scoped counter
-- migration. ALTER takes a transaction-scoped sequence lock before durable high-water
-- inspection. A pre-v0.3 replica therefore cannot allocate between the read
-- and RESTART and have its revision rewound by this migration.
-- CREATE keeps baseline-plus-migrations replayable after the current baseline
-- has already contracted the legacy sequence away.
CREATE SEQUENCE IF NOT EXISTS foghorn.source_projection_revision;
ALTER SEQUENCE foghorn.source_projection_revision INCREMENT BY 1;

DO $$
DECLARE
    durable_high_water BIGINT;
BEGIN
    SELECT GREATEST(
        COALESCE((SELECT last_value FROM foghorn.source_projection_revision), 0),
        COALESCE((SELECT MAX(source_revision) FROM foghorn.ingest_sessions), 0),
        COALESCE((SELECT MAX(source_revision) FROM foghorn.ingest_offline_effects), 0),
        COALESCE((SELECT MAX(source_revision) FROM foghorn.ingest_admission_effects), 0)
    )
    INTO durable_high_water;

    -- RESTART is transactional and the next legacy allocation gets a value
    -- strictly above every revision represented at this pre-counter phase.
    -- Migration 014 deliberately leaves this sequence untouched so old
    -- replicas remain in the low namespace during the rolling upgrade.
    EXECUTE format(
        'ALTER SEQUENCE foghorn.source_projection_revision RESTART WITH %s',
        durable_high_water + 1
    );
END
$$;
