CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_deliveries_rejected
    ON commodore.media_authority_deliveries(authority_kind ASC, authority_id ASC)
    WHERE status = 'rejected';
