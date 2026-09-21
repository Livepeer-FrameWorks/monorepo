-- A delivery records whether its envelope is a short lease when it is enqueued,
-- so the one-second short-lease claim never joins the version history.
ALTER TABLE commodore.media_authority_deliveries
    ADD COLUMN IF NOT EXISTS short_lease BOOLEAN NOT NULL DEFAULT FALSE;
