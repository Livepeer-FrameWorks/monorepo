-- DVR playback authority must survive deletion or unavailability of its parent
-- stream. Existing rows are backfilled from the current stream policy; orphaned
-- rows remain protected and therefore cannot be compiled as accidentally public.
ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS requires_auth BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS playback_policy JSONB,
    ADD COLUMN IF NOT EXISTS playback_webhook_secret_enc TEXT,
    ADD COLUMN IF NOT EXISTS playback_authority_ready BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS commodore.media_authority_counters (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    last_version BIGINT NOT NULL DEFAULT 0 CHECK (last_version >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id),
    CONSTRAINT chk_media_authority_counter_kind
        CHECK (authority_kind IN ('tenant', 'media_object'))
);

CREATE TABLE IF NOT EXISTS commodore.media_authority_versions (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    payload_schema_version INTEGER NOT NULL CHECK (payload_schema_version > 0),
    payload BYTEA NOT NULL,
    payload_sha256 BYTEA NOT NULL CHECK (octet_length(payload_sha256) = 32),
    source_revisions JSONB NOT NULL DEFAULT '[]'::jsonb,
    issued_at TIMESTAMPTZ NOT NULL,
    refresh_after TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id, authority_version),
    CONSTRAINT chk_media_authority_version_kind
        CHECK (authority_kind IN ('tenant', 'media_object')),
    CONSTRAINT chk_media_authority_version_times
        CHECK (issued_at <= refresh_after AND refresh_after < valid_until),
    CONSTRAINT chk_media_authority_source_revisions_array
        CHECK (jsonb_typeof(source_revisions) = 'array')
);

CREATE TABLE IF NOT EXISTS commodore.media_authority_current (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id),
    CONSTRAINT fk_media_authority_current_version
        FOREIGN KEY (authority_kind, authority_id, authority_version)
        REFERENCES commodore.media_authority_versions(authority_kind, authority_id, authority_version),
    CONSTRAINT chk_media_authority_current_kind
        CHECK (authority_kind IN ('tenant', 'media_object'))
);

CREATE TABLE IF NOT EXISTS commodore.media_authority_deliveries (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    authority_version BIGINT NOT NULL CHECK (authority_version > 0),
    cell_id VARCHAR(255) NOT NULL,
    signed_envelope BYTEA NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id, authority_version, cell_id),
    CONSTRAINT fk_media_authority_delivery_version
        FOREIGN KEY (authority_kind, authority_id, authority_version)
        REFERENCES commodore.media_authority_versions(authority_kind, authority_id, authority_version),
    CONSTRAINT chk_media_authority_delivery_kind
        CHECK (authority_kind IN ('tenant', 'media_object')),
    CONSTRAINT chk_media_authority_delivery_status
        CHECK (status IN ('pending', 'delivering', 'acknowledged', 'superseded')),
    CONSTRAINT chk_media_authority_delivery_cell
        CHECK (btrim(cell_id) <> '')
);

CREATE INDEX IF NOT EXISTS idx_media_authority_deliveries_pending
    ON commodore.media_authority_deliveries(next_attempt_at, created_at)
    WHERE status IN ('pending', 'delivering');

-- Target history is written when delivery is enqueued, not only after an ACK.
-- A cell that was granted while partitioned must still receive the later
-- replacement/revocation before an older pending delivery can become usable.
CREATE TABLE IF NOT EXISTS commodore.media_authority_targets (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    cell_id VARCHAR(255) NOT NULL,
    highest_targeted_version BIGINT NOT NULL CHECK (highest_targeted_version > 0),
    first_targeted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_targeted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (authority_kind, authority_id, cell_id),
    CONSTRAINT chk_media_authority_target_kind
        CHECK (authority_kind IN ('tenant', 'media_object')),
    CONSTRAINT chk_media_authority_target_cell CHECK (btrim(cell_id) <> '')
);

CREATE TABLE IF NOT EXISTS commodore.media_authority_distribution (
    authority_kind VARCHAR(32) NOT NULL,
    authority_id VARCHAR(255) NOT NULL,
    cell_id VARCHAR(255) NOT NULL,
    highest_acknowledged_version BIGINT NOT NULL CHECK (highest_acknowledged_version > 0),
    first_acknowledged_at TIMESTAMPTZ NOT NULL,
    last_acknowledged_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (authority_kind, authority_id, cell_id),
    CONSTRAINT chk_media_authority_distribution_kind
        CHECK (authority_kind IN ('tenant', 'media_object')),
    CONSTRAINT chk_media_authority_distribution_cell
        CHECK (btrim(cell_id) <> ''),
    CONSTRAINT chk_media_authority_distribution_times
        CHECK (first_acknowledged_at <= last_acknowledged_at)
);

CREATE TABLE IF NOT EXISTS commodore.media_authority_refresh_inbox (
    source_service VARCHAR(64) NOT NULL,
    source_event_id VARCHAR(255) NOT NULL,
    tenant_id UUID NOT NULL,
    reason VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_service, source_event_id),
    CONSTRAINT chk_media_authority_refresh_source
        CHECK (source_service IN ('purser', 'quartermaster', 'commodore')),
    CONSTRAINT chk_media_authority_refresh_status
        CHECK (status IN ('pending', 'processing', 'completed'))
);

CREATE INDEX IF NOT EXISTS idx_media_authority_refresh_inbox_pending
    ON commodore.media_authority_refresh_inbox(next_attempt_at, created_at)
    WHERE status <> 'completed';

CREATE OR REPLACE FUNCTION commodore.enqueue_live_stream_media_authority_refresh(
    p_stream_id UUID,
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_stream_id IS NULL OR p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO commodore.media_authority_refresh_inbox(
        source_service, source_event_id, tenant_id, reason
    ) VALUES (
        'commodore', 'live_stream:' || p_stream_id::text || ':' || gen_random_uuid()::text,
        p_tenant_id, 'media_object:live_stream:' || p_stream_id::text || ':' || p_reason
    );
END;
$$;

CREATE OR REPLACE FUNCTION commodore.live_stream_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    stream_id UUID;
    tenant_id UUID;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.tenant_id IS NOT DISTINCT FROM NEW.tenant_id
       AND OLD.user_id IS NOT DISTINCT FROM NEW.user_id
       AND OLD.internal_name IS NOT DISTINCT FROM NEW.internal_name
       AND OLD.playback_id IS NOT DISTINCT FROM NEW.playback_id
       AND OLD.stream_key IS NOT DISTINCT FROM NEW.stream_key
       AND OLD.ingest_mode IS NOT DISTINCT FROM NEW.ingest_mode
       AND OLD.always_on IS NOT DISTINCT FROM NEW.always_on
       AND OLD.is_recording_enabled IS NOT DISTINCT FROM NEW.is_recording_enabled
       AND OLD.requires_auth IS NOT DISTINCT FROM NEW.requires_auth
       AND OLD.playback_policy IS NOT DISTINCT FROM NEW.playback_policy
       AND OLD.playback_webhook_secret_enc IS NOT DISTINCT FROM NEW.playback_webhook_secret_enc
       AND OLD.active_ingest_cluster_id IS NOT DISTINCT FROM NEW.active_ingest_cluster_id
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;
    stream_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    tenant_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM commodore.enqueue_live_stream_media_authority_refresh(stream_id, tenant_id, 'stream_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_live_stream_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, user_id, internal_name, playback_id, stream_key,
    ingest_mode, always_on, is_recording_enabled, requires_auth, playback_policy,
    playback_webhook_secret_enc, active_ingest_cluster_id, deleted_at
ON commodore.streams
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_media_authority_changed();

CREATE OR REPLACE FUNCTION commodore.live_stream_child_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_stream UUID;
    affected_tenant UUID;
BEGIN
    affected_stream := CASE WHEN TG_OP = 'DELETE' THEN OLD.stream_id ELSE NEW.stream_id END;
    SELECT tenant_id INTO affected_tenant FROM commodore.streams WHERE id = affected_stream;
    PERFORM commodore.enqueue_live_stream_media_authority_refresh(affected_stream, affected_tenant, TG_ARGV[0]);
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_pull_source_media_authority
AFTER INSERT OR DELETE OR UPDATE OF source_uri_enc, enabled, allowed_cluster_ids
ON commodore.stream_pull_sources
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_child_media_authority_changed('pull_source_changed');

CREATE OR REPLACE TRIGGER trg_native_source_media_authority
AFTER INSERT OR DELETE OR UPDATE OF source_spec, source_kind, placement_count, allowed_cluster_ids
ON commodore.stream_mist_sources
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_child_media_authority_changed('native_source_changed');

CREATE OR REPLACE TRIGGER trg_push_target_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, stream_id, platform, name, target_uri, is_enabled
ON commodore.push_targets
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_child_media_authority_changed('push_target_changed');

CREATE OR REPLACE TRIGGER trg_stream_processing_media_authority
AFTER INSERT OR DELETE OR UPDATE OF processes_live, processes_dvr, processes_clip,
    processes_dvr_finalize, processes_vod
ON commodore.stream_processing_config
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_child_media_authority_changed('stream_processing_changed');

CREATE OR REPLACE FUNCTION commodore.tenant_processing_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
    affected_stream RECORD;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    FOR affected_stream IN SELECT id, tenant_id FROM commodore.streams WHERE tenant_id = affected_tenant LOOP
        PERFORM commodore.enqueue_live_stream_media_authority_refresh(affected_stream.id, affected_stream.tenant_id, 'tenant_processing_changed');
    END LOOP;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_tenant_processing_media_authority
AFTER INSERT OR UPDATE OR DELETE ON commodore.tenant_processing_config
FOR EACH ROW EXECUTE FUNCTION commodore.tenant_processing_media_authority_changed();

CREATE OR REPLACE FUNCTION commodore.signing_key_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    INSERT INTO commodore.media_authority_refresh_inbox(source_service, source_event_id, tenant_id, reason)
    VALUES (
        'commodore', 'signing_key:' || gen_random_uuid()::text, affected_tenant,
        'tenant_media_objects:signing_key_changed'
    );
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_signing_key_media_authority
AFTER INSERT OR DELETE OR UPDATE OF kid, public_key_pem, algorithm, status, revoked_at
ON commodore.signing_keys
FOR EACH ROW EXECUTE FUNCTION commodore.signing_key_media_authority_changed();

CREATE OR REPLACE FUNCTION commodore.enqueue_artifact_media_authority_refresh(
    p_artifact_id UUID,
    p_tenant_id UUID,
    p_kind TEXT,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_artifact_id IS NULL OR p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO commodore.media_authority_refresh_inbox(source_service, source_event_id, tenant_id, reason)
    VALUES (
        'commodore', p_kind || ':' || p_artifact_id::text || ':' || gen_random_uuid()::text,
        p_tenant_id, 'media_object:' || p_kind || ':' || p_artifact_id::text || ':' || p_reason
    );
END;
$$;

CREATE OR REPLACE FUNCTION commodore.artifact_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    artifact_id UUID;
    tenant_id UUID;
    old_authority JSONB;
    new_authority JSONB;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_authority := jsonb_build_object(
            'tenant_id', to_jsonb(OLD)->'tenant_id', 'user_id', to_jsonb(OLD)->'user_id',
            'stream_id', to_jsonb(OLD)->'stream_id', 'internal_name', to_jsonb(OLD)->'internal_name',
            'playback_id', to_jsonb(OLD)->'playback_id', 'origin_cluster_id', to_jsonb(OLD)->'origin_cluster_id',
            'requires_auth', to_jsonb(OLD)->'requires_auth', 'playback_policy', to_jsonb(OLD)->'playback_policy',
            'playback_webhook_secret_enc', to_jsonb(OLD)->'playback_webhook_secret_enc',
            'artifact_hash', COALESCE(to_jsonb(OLD)->'clip_hash', to_jsonb(OLD)->'dvr_hash', to_jsonb(OLD)->'vod_hash'),
            'origin_type', to_jsonb(OLD)->'origin_type'
        );
        new_authority := jsonb_build_object(
            'tenant_id', to_jsonb(NEW)->'tenant_id', 'user_id', to_jsonb(NEW)->'user_id',
            'stream_id', to_jsonb(NEW)->'stream_id', 'internal_name', to_jsonb(NEW)->'internal_name',
            'playback_id', to_jsonb(NEW)->'playback_id', 'origin_cluster_id', to_jsonb(NEW)->'origin_cluster_id',
            'requires_auth', to_jsonb(NEW)->'requires_auth', 'playback_policy', to_jsonb(NEW)->'playback_policy',
            'playback_webhook_secret_enc', to_jsonb(NEW)->'playback_webhook_secret_enc',
            'artifact_hash', COALESCE(to_jsonb(NEW)->'clip_hash', to_jsonb(NEW)->'dvr_hash', to_jsonb(NEW)->'vod_hash'),
            'origin_type', to_jsonb(NEW)->'origin_type'
        );
        IF old_authority = new_authority THEN
            RETURN NEW;
        END IF;
    END IF;
    artifact_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    tenant_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM commodore.enqueue_artifact_media_authority_refresh(artifact_id, tenant_id, TG_ARGV[0], 'artifact_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_clip_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, user_id, stream_id, clip_hash, internal_name,
    playback_id, origin_cluster_id, requires_auth, playback_policy, playback_webhook_secret_enc
ON commodore.clips
FOR EACH ROW EXECUTE FUNCTION commodore.artifact_media_authority_changed('clip');

CREATE OR REPLACE TRIGGER trg_dvr_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, user_id, stream_id, dvr_hash, internal_name,
    playback_id, origin_cluster_id, requires_auth, playback_policy, playback_webhook_secret_enc
ON commodore.dvr_recordings
FOR EACH ROW EXECUTE FUNCTION commodore.artifact_media_authority_changed('dvr');

CREATE OR REPLACE TRIGGER trg_vod_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, user_id, stream_id, vod_hash, internal_name,
    playback_id, origin_cluster_id, origin_type, requires_auth, playback_policy, playback_webhook_secret_enc
ON commodore.vod_assets
FOR EACH ROW EXECUTE FUNCTION commodore.artifact_media_authority_changed('vod');
