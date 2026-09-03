ALTER TABLE periscope.stream_health_samples
    ADD COLUMN IF NOT EXISTS max_keepaway_ms Nullable(UInt32) AFTER buffer_size;
