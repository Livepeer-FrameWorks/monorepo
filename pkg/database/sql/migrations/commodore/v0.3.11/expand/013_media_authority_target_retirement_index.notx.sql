CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_targets_retired
    ON commodore.media_authority_targets(correction_until ASC, authority_kind ASC, authority_id ASC, cell_id ASC)
    WHERE correction_until IS NOT NULL;
