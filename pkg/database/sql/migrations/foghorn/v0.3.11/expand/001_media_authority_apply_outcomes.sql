-- Store.Apply records stale-version and terminal-lifecycle rejections as their
-- own audit outcomes. Without them in the allowed set the audit insert fails,
-- the apply transaction rolls back, and the sender retries a rejection it can
-- never learn about.
ALTER TABLE foghorn.media_authority_apply_audit
    DROP CONSTRAINT IF EXISTS chk_media_authority_apply_outcome;
ALTER TABLE foghorn.media_authority_apply_audit
    ADD CONSTRAINT chk_media_authority_apply_outcome
    CHECK (outcome IN (
        'applied', 'duplicate', 'stale_version_rejected', 'rollback_rejected',
        'conflict_rejected', 'terminal_lifecycle_rejected', 'verification_rejected'
    )) NOT VALID;
