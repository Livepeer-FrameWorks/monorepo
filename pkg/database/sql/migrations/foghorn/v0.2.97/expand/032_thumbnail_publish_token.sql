-- Token-fenced publication: a lease TOKEN + per-token CANDIDATE object keys.
--
-- publish_lease_token is the holder token AcquireThumbnailPublishLease mints; every post-claim settlement
-- (MarkThumbnailObjectVerifiedToken, EnterThumbnailPublishingToken, PublishThumbnailAttemptToken) requires an exact,
-- non-null match on it, so a STALE holder (its lease expired and a peer re-acquired) matches zero rows and cannot
-- publish. The promoted object is written to a per-token candidate key `thumbnails/{asset}/v/{token}/{file}` (the
-- version segment IS the token), so a stale holder can only ever write its OWN private candidate.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS publish_lease_token TEXT;

-- A row may only be in a token-gated state ('publishing'/'published') if it carries a nonblank lease token — the
-- DB fact behind the token contract, so no path (including the generic status transition) can create tokenless
-- publication state. Idempotent add (skips if the constraint already exists).
-- Added NOT VALID (expand-phase safe: no full-table scan/lock on add), then VALIDATEd — instant here because this
-- token-fenced publication has not shipped, so no rows can violate it. The end state is a validated constraint that
-- matches the baseline.
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_foghorn_thumbnail_publishing_requires_token'
    ) THEN
        ALTER TABLE foghorn.thumbnail_task_assignment
            ADD CONSTRAINT chk_foghorn_thumbnail_publishing_requires_token
            CHECK (status NOT IN ('publishing','published') OR (publish_lease_token IS NOT NULL AND publish_lease_token <> '')) NOT VALID;
        ALTER TABLE foghorn.thumbnail_task_assignment
            VALIDATE CONSTRAINT chk_foghorn_thumbnail_publishing_requires_token;
    END IF;
END $$;

-- active_token is the SERVED version segment for the current winner (the token whose candidate the pointer serves).
-- A publish REQUIRES a non-empty lease token, so a published pointer always has active_token set and the resolver
-- serves it directly. active_version keeps the attempt_id purely for the FK cascade (deleting the attempt cascades
-- the pointer) and the monotonic CAS/GC anchor — it is not the served segment.
ALTER TABLE foghorn.thumbnail_active_pointer
    ADD COLUMN IF NOT EXISTS active_token TEXT;
