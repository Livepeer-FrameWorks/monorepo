-- ============================================================================
-- OPERATOR-ANALYTICS READ-ONLY ROLE - commodore grants
-- ============================================================================
-- See analytics_ro_quartermaster.sql for the role contract. Fail-closed
-- allowlist: auth material (password/reset/verification tokens, refresh
-- tokens, auth codes, stream keys, signing keys, encrypted push URIs) is
-- never granted.
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'frameworks_analytics_ro') THEN
        CREATE ROLE frameworks_analytics_ro LOGIN;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA commodore TO frameworks_analytics_ro;

GRANT SELECT ON
    commodore.clips,
    commodore.dvr_recordings,
    commodore.vod_assets,
    commodore.dvr_chapter_playback,
    commodore.tenant_processing_config,
    commodore.stream_processing_config,
    commodore.tenant_media_retention_policies,
    commodore.pull_source_events,
    commodore.wallet_identities,
    commodore.stream_cluster_pins,
    commodore.service_event_outbox,
    commodore.signing_key_audit
TO frameworks_analytics_ro;

-- password_hash and the plaintext verification/reset tokens stay private.
GRANT SELECT (
    id, tenant_id, email, first_name, last_name,
    role, permissions, verified, is_active,
    last_login_at, created_at, updated_at
) ON commodore.users TO frameworks_analytics_ro;

-- token_value is the credential (hashed, but still an auth artifact).
GRANT SELECT (
    id, tenant_id, user_id, token_name, permissions,
    is_active, last_used_at, expires_at, created_at, updated_at
) ON commodore.api_tokens TO frameworks_analytics_ro;

-- stream_key is a live ingest credential; identifiers and metadata only.
GRANT SELECT (
    id, tenant_id, user_id, playback_id, internal_name,
    title, description, is_recording_enabled,
    dvr_chapter_mode, dvr_chapter_interval_seconds,
    ingest_mode, always_on,
    dvr_retention_days_override, clip_retention_days_override,
    active_ingest_cluster_id, active_ingest_cluster_updated_at,
    created_at, updated_at
) ON commodore.streams TO frameworks_analytics_ro;

-- target_uri embeds the destination stream key (encrypted at rest, but
-- still excluded); runtime state is what analytics wants.
GRANT SELECT (
    id, tenant_id, stream_id, platform, name, is_enabled,
    status, last_error, last_pushed_at, created_at, updated_at
) ON commodore.push_targets TO frameworks_analytics_ro;
