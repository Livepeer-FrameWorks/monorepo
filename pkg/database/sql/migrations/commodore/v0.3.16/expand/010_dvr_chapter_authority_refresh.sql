-- Chapter policy is consumed from signed media authority at publisher admission.
-- A policy-only edit must advance the durable refresh revision, including while
-- a creation compile is in flight.
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
       AND OLD.live_video_abr IS NOT DISTINCT FROM NEW.live_video_abr
       AND OLD.transcode_ladder IS NOT DISTINCT FROM NEW.transcode_ladder
       AND OLD.always_on IS NOT DISTINCT FROM NEW.always_on
       AND OLD.is_recording_enabled IS NOT DISTINCT FROM NEW.is_recording_enabled
       AND OLD.dvr_chapter_mode IS NOT DISTINCT FROM NEW.dvr_chapter_mode
       AND OLD.dvr_chapter_interval_seconds IS NOT DISTINCT FROM NEW.dvr_chapter_interval_seconds
       AND OLD.requires_auth IS NOT DISTINCT FROM NEW.requires_auth
       AND OLD.playback_policy IS NOT DISTINCT FROM NEW.playback_policy
       AND OLD.playback_webhook_secret_enc IS NOT DISTINCT FROM NEW.playback_webhook_secret_enc
       -- The authority carries the effective origin, not the claim: an ingest
       -- starting or stopping on the cluster the stream last ingested on
       -- changes nothing a cell holds.
       AND COALESCE(NULLIF(OLD.active_ingest_cluster_id, ''), NULLIF(OLD.last_ingest_cluster_id, ''))
           IS NOT DISTINCT FROM COALESCE(NULLIF(NEW.active_ingest_cluster_id, ''), NULLIF(NEW.last_ingest_cluster_id, ''))
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;
    stream_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    tenant_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM commodore.enqueue_live_stream_media_authority_refresh(stream_id, tenant_id, 'stream_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

DROP TRIGGER IF EXISTS trg_live_stream_media_authority ON commodore.streams;
CREATE TRIGGER trg_live_stream_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tenant_id, user_id, internal_name, playback_id, stream_key,
    ingest_mode, live_video_abr, transcode_ladder, always_on, is_recording_enabled, dvr_chapter_mode, dvr_chapter_interval_seconds,
    requires_auth, playback_policy,
    playback_webhook_secret_enc, active_ingest_cluster_id, last_ingest_cluster_id, deleted_at
ON commodore.streams
FOR EACH ROW EXECUTE FUNCTION commodore.live_stream_media_authority_changed();

-- Repair existing stream policies through the normal compiler. Auto-record may
-- be disabled on streams whose saved chapter settings are used by manual starts.
-- Each obligation carries its owning tenant and preserves any active compile lease.
SELECT commodore.enqueue_live_stream_media_authority_refresh(
    streams.id, streams.tenant_id, 'dvr_chapter_policy_repair'
)
FROM commodore.streams AS streams
WHERE streams.tenant_id IS NOT NULL
  AND streams.deleted_at IS NULL;
