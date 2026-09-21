-- Trigger-enqueued changes of one (tenant, reason) fold into a single unfinished
-- row through coalesce_key. Existing rows keep a NULL key and are never folded
-- into, so the unique index below cannot fail on rows enqueued before it.
ALTER TABLE quartermaster.media_authority_refresh_outbox
    ADD COLUMN IF NOT EXISTS coalesce_key VARCHAR(320);
ALTER TABLE quartermaster.media_authority_refresh_outbox
    ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE quartermaster.media_authority_refresh_outbox
    ADD COLUMN IF NOT EXISTS pending_since TIMESTAMPTZ NOT NULL DEFAULT NOW();
