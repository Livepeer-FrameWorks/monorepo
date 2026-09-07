-- Field re-encryption changes storage, not media authority. The data migration
-- sets this transaction-local flag on each UPDATE statement to avoid issuing a
-- target-set revision and fanout for every rewritten row.
CREATE TABLE IF NOT EXISTS commodore.field_encryption_quarantine (
    table_name TEXT NOT NULL,
    column_name TEXT NOT NULL,
    row_id TEXT NOT NULL,
    ciphertext_fingerprint TEXT NOT NULL,
    purpose TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT 'decrypt_failed',
    attempts BIGINT NOT NULL DEFAULT 1,
    first_observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (table_name, column_name, row_id, ciphertext_fingerprint)
);

CREATE OR REPLACE FUNCTION commodore.live_stream_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    stream_id UUID;
    tenant_id UUID;
BEGIN
    IF current_setting('frameworks.suppress_media_authority_refresh', true) = 'on' THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

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

CREATE OR REPLACE FUNCTION commodore.live_stream_child_media_authority_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_stream UUID;
    affected_tenant UUID;
BEGIN
    IF current_setting('frameworks.suppress_media_authority_refresh', true) = 'on' THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

    affected_stream := CASE WHEN TG_OP = 'DELETE' THEN OLD.stream_id ELSE NEW.stream_id END;
    SELECT tenant_id INTO affected_tenant FROM commodore.streams WHERE id = affected_stream;
    PERFORM commodore.enqueue_live_stream_media_authority_refresh(affected_stream, affected_tenant, TG_ARGV[0]);
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
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
    IF current_setting('frameworks.suppress_media_authority_refresh', true) = 'on' THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

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
