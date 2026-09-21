CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_distribution_cell_ack
    ON commodore.media_authority_distribution(cell_id ASC, authority_kind COLLATE "C" ASC, authority_id COLLATE "C" ASC)
    INCLUDE (highest_acknowledged_version, last_acknowledged_at);
