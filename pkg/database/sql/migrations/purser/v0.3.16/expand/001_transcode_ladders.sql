-- The named transcode ladders a tier allows (each with its video renditions)
-- and the one its tenants get unless they pick another. purser bootstrap
-- reconciles both from the embedded tier catalog. A ladder's renditions are
-- part of every stream's process config, so a change to either refreshes the
-- media authority of the tier's tenants like a change to processes_*.
ALTER TABLE purser.billing_tiers
    ADD COLUMN IF NOT EXISTS transcode_ladders JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS default_transcode_ladder VARCHAR(64) NOT NULL DEFAULT '';

CREATE OR REPLACE TRIGGER trg_billing_tier_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tier_name, tier_level, is_active, features,
    processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod,
    transcode_ladders, default_transcode_ladder
ON purser.billing_tiers
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_billing_tier_changed();
