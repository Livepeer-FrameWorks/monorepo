-- Durability tracking for two stranded-state recovery paths.
--
-- foghorn.artifacts.dvr_start_dispatch: descriptor persisted with the DVR
-- 'requested'->'starting' transition (target node + fields to rebuild the
-- DVRStartRequest + a 'state' key). jobs.DVRStartingRecoveryJob reads it to
-- idempotently re-dispatch (or finalize) a recording whose node ack never
-- arrived, so a 'starting' row is never permanently strandable.
--
-- foghorn.vod_metadata.processes_json: requested processing spec captured at
-- CompleteVodUpload, read by the completing-VOD recovery job so a recovered
-- upload reproduces the SAME requested outputs instead of default processing.
ALTER TABLE foghorn.artifacts
  ADD COLUMN IF NOT EXISTS dvr_start_dispatch JSONB;

ALTER TABLE foghorn.vod_metadata
  ADD COLUMN IF NOT EXISTS processes_json TEXT;
