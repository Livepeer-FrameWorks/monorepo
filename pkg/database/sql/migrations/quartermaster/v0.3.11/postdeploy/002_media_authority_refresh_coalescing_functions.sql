CREATE OR REPLACE FUNCTION quartermaster.enqueue_media_authority_refresh(
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO quartermaster.media_authority_refresh_outbox(source_event_id, tenant_id, reason, coalesce_key)
    VALUES (p_reason || ':' || gen_random_uuid()::text, p_tenant_id, p_reason, p_tenant_id::text || ':' || p_reason)
    ON CONFLICT (coalesce_key) WHERE status <> 'completed'
    DO UPDATE SET
        revision = quartermaster.media_authority_refresh_outbox.revision + 1,
        status = 'pending',
        next_attempt_at = NOW(),
        completed_at = NULL,
        last_error = NULL,
        -- The age of the oldest unfinished obligation, not of the latest fold: a
        -- superseding revision must not make a stuck queue look young again.
        pending_since = LEAST(
            quartermaster.media_authority_refresh_outbox.pending_since,
            EXCLUDED.pending_since
        ),
        -- An active delivery lease stays as a short serialization fence. The
        -- claimed revision can no longer complete this row, and the replacement
        -- revision becomes claimable as soon as that lease ends.
        lease_expires_at = CASE
            WHEN quartermaster.media_authority_refresh_outbox.status = 'delivering'
            THEN quartermaster.media_authority_refresh_outbox.lease_expires_at
            ELSE NULL
        END,
        updated_at = NOW();
END;
$$;

CREATE OR REPLACE FUNCTION quartermaster.media_authority_tenant_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    PERFORM quartermaster.enqueue_media_authority_refresh(affected_tenant, 'tenant_authority_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE FUNCTION quartermaster.media_authority_cluster_access_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM quartermaster.enqueue_media_authority_refresh(affected_tenant, 'cluster_access_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE FUNCTION quartermaster.media_authority_cluster_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_cluster TEXT;
    new_cluster TEXT;
    affected RECORD;
BEGIN
    -- An UPDATE that rewrites a row with its own values changes no authority.
    IF TG_OP = 'UPDATE' AND to_jsonb(OLD) - 'updated_at' = to_jsonb(NEW) - 'updated_at' THEN
        RETURN NEW;
    END IF;
    old_cluster := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN OLD.cluster_id ELSE NULL END;
    new_cluster := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN NEW.cluster_id ELSE NULL END;
    FOR affected IN
        SELECT DISTINCT access.tenant_id
        FROM quartermaster.tenant_cluster_access AS access
        WHERE access.cluster_id IN (old_cluster, new_cluster)
    LOOP
        PERFORM quartermaster.enqueue_media_authority_refresh(affected.tenant_id, 'cluster_authority_changed');
    END LOOP;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
