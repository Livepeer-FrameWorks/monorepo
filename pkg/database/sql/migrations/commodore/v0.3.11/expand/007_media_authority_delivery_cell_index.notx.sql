CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_deliveries_cell_due
    ON commodore.media_authority_deliveries(cell_id ASC, replay ASC, next_attempt_at ASC, created_at ASC)
    WHERE NOT short_lease AND status IN ('pending', 'delivering');
