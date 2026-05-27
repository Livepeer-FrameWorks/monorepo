-- Lifecycle-specific MistServer process overrides. Commodore resolves the
-- lifecycle snapshot and Foghorn stores/applies it verbatim.

ALTER TABLE commodore.tenant_processing_config
  ADD COLUMN IF NOT EXISTS processes_dvr JSONB,
  ADD COLUMN IF NOT EXISTS processes_clip JSONB,
  ADD COLUMN IF NOT EXISTS processes_dvr_finalize JSONB;

ALTER TABLE commodore.stream_processing_config
  ADD COLUMN IF NOT EXISTS processes_dvr JSONB,
  ADD COLUMN IF NOT EXISTS processes_clip JSONB,
  ADD COLUMN IF NOT EXISTS processes_dvr_finalize JSONB;
