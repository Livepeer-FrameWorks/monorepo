-- Chapter VOD rows are derived from DVRs and must inherit the DVR's snapshotted
-- playback policy. Existing rows are repaired by the resumable
-- commodore_chapter_playback_authority_v0_3_0 data migration.
ALTER TABLE commodore.vod_assets
    ALTER COLUMN requires_auth SET DEFAULT TRUE;
