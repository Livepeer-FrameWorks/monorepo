CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_delegated_jwt_replays_expires_at_range
    ON commodore.delegated_jwt_replays (expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_auth_authz_codes_expires_range
    ON commodore.auth_authorization_codes(expires_at ASC)
    WHERE consumed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_auth_device_codes_expires_range
    ON commodore.auth_device_codes(expires_at ASC)
    WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_wallet_auth_challenges_expiry_range
    ON commodore.wallet_auth_challenges(expires_at ASC)
    WHERE consumed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_streams_deleting_range ON commodore.streams(deleted_at ASC) WHERE deleted_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_clips_created_range ON commodore.clips(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_dvr_created_range ON commodore.dvr_recordings(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_vod_created_range ON commodore.vod_assets(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_creation_intents_pending_range
    ON commodore.artifact_creation_intents(updated_at ASC)
    WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_creation_intents_ack_pending_range
    ON commodore.artifact_creation_intents(command_ack_next_at ASC)
    WHERE command_ack_pending = TRUE;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_invalidation_outbox_pending_range
    ON commodore.playback_policy_invalidation_outbox(next_attempt_at ASC)
    WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_service_event_outbox_pending_range
    ON commodore.service_event_outbox(created_at ASC)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_domain_event_outbox_pending_range
    ON commodore.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_domain_event_outbox_completed_range
    ON commodore.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_stream_cleanup_outbox_pending_range
    ON commodore.stream_cleanup_outbox(next_attempt_at ASC)
    WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_commodore_account_email_outbox_pending_range
    ON commodore.account_email_outbox(next_attempt_at ASC)
    WHERE status = 'pending';
