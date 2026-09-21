ALTER TABLE commodore.media_authority_targets
    ADD COLUMN IF NOT EXISTS correction_until TIMESTAMPTZ;
