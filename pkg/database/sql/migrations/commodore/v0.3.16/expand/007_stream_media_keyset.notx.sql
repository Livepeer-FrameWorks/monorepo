CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_dvr_stream_history
    ON commodore.dvr_recordings(tenant_id,stream_id,created_at DESC,id DESC) WHERE stream_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_clips_stream_history
    ON commodore.clips(tenant_id,stream_id,created_at DESC,id DESC) WHERE stream_id IS NOT NULL;
