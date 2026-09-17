CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_purser_media_authority_refresh_due_v2
    ON purser.media_authority_refresh_outbox(next_attempt_at ASC, created_at ASC)
    WHERE status <> 'completed';
