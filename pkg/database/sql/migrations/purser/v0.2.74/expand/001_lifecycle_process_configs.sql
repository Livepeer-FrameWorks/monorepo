-- Lifecycle-specific MistServer process defaults. Commodore is the policy
-- authority; Foghorn stores/applies resolved snapshots and must not derive
-- lifecycle configs locally.

ALTER TABLE purser.billing_tiers
  ADD COLUMN IF NOT EXISTS processes_dvr JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_clip JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_dvr_finalize JSONB DEFAULT '[]';
