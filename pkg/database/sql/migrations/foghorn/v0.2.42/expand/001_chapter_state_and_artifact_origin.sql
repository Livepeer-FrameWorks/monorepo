-- DVR chapters become finalized VOD-type artifacts. Chapter rows stay
-- as range metadata but gain an explicit state machine + a foreign key
-- to the playback artifact that finalization produces.
--
-- foghorn.artifacts gains origin metadata so hidden chapter-origin
-- artifacts (library_visible=false) inherit the parent DVR's policy
-- and surface under DVR APIs rather than the user's media library.
--
-- The unique partial index on (origin_id) WHERE origin_type='dvr_chapter'
-- enforces idempotent finalization: retries that reuse the same
-- chapter_id find the existing artifact row and don't leak hidden
-- duplicates.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql

-- ============================================================================
-- foghorn.dvr_chapters — state machine + finalized artifact link
-- ============================================================================

ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS state                  VARCHAR(32) NOT NULL DEFAULT 'open',
    ADD COLUMN IF NOT EXISTS playback_artifact_hash VARCHAR(32),
    ADD COLUMN IF NOT EXISTS finalize_attempts      INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_failure_reason    TEXT,
    ADD COLUMN IF NOT EXISTS reclaim_started_at     TIMESTAMPTZ;

-- No backfill: this is a clean-slate refactor and no production chapter
-- rows exist (the previous chapter model wrote no persistent rows yet).
-- Any rows in flight at deploy time keep the DEFAULT 'open' state and
-- get re-classified by the chapter sweeper on its first tick.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'chk_foghorn_dvr_chapters_state'
    ) THEN
        -- NOT VALID skips existing-row validation; VALIDATE CONSTRAINT
        -- fires in a postdeploy migration. New writes are checked
        -- immediately.
        ALTER TABLE foghorn.dvr_chapters
            ADD CONSTRAINT chk_foghorn_dvr_chapters_state CHECK (state IN (
                'open', 'closed', 'finalizing', 'finalized', 'frozen',
                'reclaimed', 'failed_source_missing', 'failed_permanent'
            )) NOT VALID;
    END IF;
END$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'fk_foghorn_dvr_chapters_playback_artifact'
    ) THEN
        ALTER TABLE foghorn.dvr_chapters
            ADD CONSTRAINT fk_foghorn_dvr_chapters_playback_artifact
            FOREIGN KEY (playback_artifact_hash)
            REFERENCES foghorn.artifacts(artifact_hash)
            ON DELETE SET NULL
            NOT VALID;
    END IF;
END$$;

CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_pending
    ON foghorn.dvr_chapters(state)
    WHERE state IN ('closed', 'finalizing');

CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_chapters_reclaim
    ON foghorn.dvr_chapters(state, reclaim_started_at)
    WHERE state = 'frozen';

-- ============================================================================
-- foghorn.artifacts — origin metadata + library visibility
-- ============================================================================

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS origin_type     VARCHAR(32),
    ADD COLUMN IF NOT EXISTS origin_id       VARCHAR(64),
    ADD COLUMN IF NOT EXISTS library_visible BOOLEAN NOT NULL DEFAULT true;

-- Idempotent finalization: a single chapter_id maps to at most one
-- chapter-origin artifact. Retries hit the existing row and reuse
-- artifact_hash rather than leaking hidden VOD artifacts.
CREATE UNIQUE INDEX IF NOT EXISTS uq_foghorn_artifacts_chapter_origin
    ON foghorn.artifacts(origin_id)
    WHERE origin_type = 'dvr_chapter';

CREATE INDEX IF NOT EXISTS idx_foghorn_artifacts_origin
    ON foghorn.artifacts(origin_type, origin_id)
    WHERE origin_type IS NOT NULL;
