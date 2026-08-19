-- A published active pointer ALWAYS has a served token (a publish REQUIRES a non-empty lease token), so enforce
-- active_token presence as a DB fact — a NULL would otherwise silently fall back to the legacy path, masking corrupt
-- state. Added NOT VALID then VALIDATEd (instant: this token-fenced publication has not shipped, so no NULL rows).
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_foghorn_active_token_present') THEN
        ALTER TABLE foghorn.thumbnail_active_pointer
            ADD CONSTRAINT chk_foghorn_active_token_present CHECK (active_token IS NOT NULL) NOT VALID;
        ALTER TABLE foghorn.thumbnail_active_pointer VALIDATE CONSTRAINT chk_foghorn_active_token_present;
    END IF;
END $$;
