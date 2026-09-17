CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_deliveries_due_v2
    ON commodore.media_authority_deliveries(next_attempt_at ASC, created_at ASC)
    WHERE status IN ('pending', 'delivering');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_versions_expiry_v2
    ON commodore.media_authority_versions(valid_until ASC, authority_kind ASC, authority_id ASC, authority_version ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_refresh_inbox_due_v2
    ON commodore.media_authority_refresh_inbox(next_attempt_at ASC, created_at ASC)
    WHERE status <> 'completed';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_refresh_inbox_completed_v2
    ON commodore.media_authority_refresh_inbox(completed_at ASC, source_service ASC, source_event_id ASC)
    WHERE status = 'completed';
