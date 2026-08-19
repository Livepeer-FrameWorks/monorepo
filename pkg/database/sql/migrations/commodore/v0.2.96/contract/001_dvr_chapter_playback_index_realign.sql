-- Replace the released functional lower(playback_id::text) unique indexes on the artifact catalog and
-- chapter playback tables with plain btrees on the CITEXT playback_id. The resolvers query
-- `WHERE playback_id = $1` (CITEXT equality), which a lower() functional index cannot serve, so the
-- released indexes fell back to scans. The released indexes share these names, so a same-name
-- CREATE IF NOT EXISTS in expand is inert on an upgrade; the DROP+CREATE runs here in contract, where
-- DROP is allowed and the drop precedes the plain recreate so an index always covers the lookup for a
-- completed upgrade. IF EXISTS / IF NOT EXISTS keep the step reconcilable on re-apply.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.

DROP INDEX IF EXISTS commodore.idx_commodore_clips_playback_ci;
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_clips_playback_ci
    ON commodore.clips(playback_id);

DROP INDEX IF EXISTS commodore.idx_commodore_dvr_playback_ci;
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_playback_ci
    ON commodore.dvr_recordings(playback_id);

DROP INDEX IF EXISTS commodore.idx_commodore_vod_playback_ci;
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_vod_playback_ci
    ON commodore.vod_assets(playback_id);

DROP INDEX IF EXISTS commodore.idx_commodore_dvr_chapter_playback_pid_ci;
CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_pid_ci
    ON commodore.dvr_chapter_playback(playback_id);
