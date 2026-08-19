-- Reconcile the artifact catalog and chapter playback indexes to the functional form supported by
-- both PostgreSQL and YugabyteDB. YugabyteDB rejects a plain index on the CITEXT user-defined type,
-- while lower(playback_id::text) preserves case-insensitive uniqueness and is usable by resolvers
-- that query the same expression. Drop only an existing non-functional definition: this converts a
-- PostgreSQL database initialized from the original v0.2.96 baseline without disturbing the released
-- YugabyteDB functional indexes. CREATE IF NOT EXISTS also repairs a partially applied attempt where
-- an earlier DROP persisted before YugabyteDB rejected the plain replacement.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.

DO $$
DECLARE
    v_index_name TEXT;
BEGIN
    FOREACH v_index_name IN ARRAY ARRAY[
        'idx_commodore_clips_playback_ci',
        'idx_commodore_dvr_playback_ci',
        'idx_commodore_vod_playback_ci',
        'idx_commodore_dvr_chapter_playback_pid_ci'
    ]
    LOOP
        IF EXISTS (
            SELECT 1
              FROM pg_indexes
             WHERE schemaname = 'commodore'
               AND indexname = v_index_name
               AND position('lower(' IN lower(indexdef)) = 0
        ) THEN
            EXECUTE format('DROP INDEX commodore.%I', v_index_name);
        END IF;
    END LOOP;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_clips_playback_ci
    ON commodore.clips((lower(playback_id::text)));

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_playback_ci
    ON commodore.dvr_recordings((lower(playback_id::text)));

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_vod_playback_ci
    ON commodore.vod_assets((lower(playback_id::text)));

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_pid_ci
    ON commodore.dvr_chapter_playback((lower(playback_id::text)));
