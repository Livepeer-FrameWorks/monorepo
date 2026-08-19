ALTER TABLE skipper.skipper_usage
    ADD COLUMN IF NOT EXISTS provider TEXT,
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS published_at TIMESTAMP WITH TIME ZONE;

CREATE INDEX IF NOT EXISTS skipper_usage_publish_pending_idx
    ON skipper.skipper_usage (created_at)
    WHERE published_at IS NULL;
