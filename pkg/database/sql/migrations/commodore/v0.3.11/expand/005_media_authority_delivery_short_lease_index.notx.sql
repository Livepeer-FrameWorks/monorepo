CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_deliveries_short_lease_due
    ON commodore.media_authority_deliveries(cell_id ASC, next_attempt_at ASC, created_at ASC)
    WHERE short_lease AND status IN ('pending', 'delivering');
