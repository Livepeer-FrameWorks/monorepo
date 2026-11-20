CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_ingest_stop_completed
 ON commodore.media_authority_refresh_obligations(updated_at ASC,tenant_id,target_key)
 WHERE lane='ingest_stop' AND status='completed';
