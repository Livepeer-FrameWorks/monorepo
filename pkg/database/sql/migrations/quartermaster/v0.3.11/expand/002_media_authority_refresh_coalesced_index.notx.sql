CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_quartermaster_media_authority_refresh_coalesced
    ON quartermaster.media_authority_refresh_outbox(coalesce_key)
    WHERE status <> 'completed';
