ALTER TABLE purser.media_authority_refresh_outbox
    ADD COLUMN IF NOT EXISTS pending_since TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE OR REPLACE FUNCTION purser.enqueue_media_authority_refresh(
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    VALUES (p_reason || ':' || gen_random_uuid()::text, p_tenant_id, p_reason)
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending',
        next_attempt_at = NOW(),
        completed_at = NULL,
        last_error = NULL,
        -- This is the age of the oldest still-unfinished obligation, not the
        -- latest enqueue. Superseding revisions must not make a stuck queue look
        -- young again.
        pending_since = LEAST(
            purser.media_authority_refresh_outbox.pending_since,
            EXCLUDED.pending_since
        ),
        -- Keep an active delivery lease as a short serialization fence. The
        -- claimed revision can no longer complete this row, and the replacement
        -- revision becomes claimable as soon as that lease ends.
        lease_expires_at = CASE
            WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at
            ELSE NULL
        END,
        updated_at = NOW();
END;
$$;
