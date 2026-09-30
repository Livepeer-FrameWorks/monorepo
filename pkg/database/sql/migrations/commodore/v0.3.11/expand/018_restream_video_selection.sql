ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS live_video_abr VARCHAR(16) NOT NULL DEFAULT 'INHERIT'
        CONSTRAINT chk_streams_live_video_abr CHECK (live_video_abr IN ('INHERIT', 'OFF'));

ALTER TABLE commodore.push_targets
    ADD COLUMN IF NOT EXISTS video_choice VARCHAR(24) NOT NULL DEFAULT 'AUTO'
        CONSTRAINT chk_push_targets_video_choice CHECK (video_choice IN ('AUTO', 'SOURCE_VIDEO', 'PROCESSED_VIDEO'));
