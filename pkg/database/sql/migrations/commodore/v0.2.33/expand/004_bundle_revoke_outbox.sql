-- bundle_revoke watermark columns on commodore.playback_policy_invalidation_outbox.
-- Commodore enqueues a row with reason='bundle_revoke', stream_id set, and
-- bundle_min_version carrying the minimum-acceptable bundle_version when a
-- plan downgrade or cluster-access deactivation invalidates signed policy
-- bundles. Foghorn's InvalidatePlaybackAuth handler dispatches the row and
-- calls policybundle.Cache.BumpWatermark on receipt, invalidating any
-- cached bundle below the watermark.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.playback_policy_invalidation_outbox
    ADD COLUMN IF NOT EXISTS bundle_min_version BIGINT,
    ADD COLUMN IF NOT EXISTS stream_id UUID;
