ALTER TABLE processing_5m
    ADD COLUMN IF NOT EXISTS stream_id UUID DEFAULT toUUIDOrZero('') AFTER cluster_id;
