-- Bind each DVR chapter finalization attempt to the exact resolved process
-- contract dispatched to its assigned node. This is intentionally token-free;
-- Foghorn mints the short wire capability only when Mist requests the config.
ALTER TABLE foghorn.dvr_chapters
    ADD COLUMN IF NOT EXISTS finalize_processes_json TEXT;
