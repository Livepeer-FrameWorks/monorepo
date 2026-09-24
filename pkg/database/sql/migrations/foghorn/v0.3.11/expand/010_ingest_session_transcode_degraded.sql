-- First time Mist replaced an ingest session's failed transcode process with
-- local renditions (PROCESS_REPLACE), and the failed process's exit reason.
ALTER TABLE foghorn.ingest_sessions
    ADD COLUMN IF NOT EXISTS transcode_degraded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS transcode_degraded_reason TEXT;
