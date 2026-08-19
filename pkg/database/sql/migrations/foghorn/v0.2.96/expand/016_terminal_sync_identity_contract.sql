-- A trigger enqueues the abandoned attempt's staging + published-candidate objects for durable cleanup and
-- then clears the freeze + .dtsh attempt identity (all four columns) whenever an artifact transitions to a
-- terminal status via any writer, so a late completion matches nothing and stale recovery can never rewrite
-- the terminal row; sync_object_key is retained for purge cleanup. Five scoped CHECK constraints codify the
-- invariant (all NULL-safe): paired nullability of the main and .dtsh identity pairs; terminal status implies
-- all four identity columns null; an active main freeze identity implies the ready+freezing+in_progress state
-- with a bound nonblank descriptor; and an active .dtsh identity implies the in_progress-dtsh, synced,
-- not-yet-dtsh-synced state.
--
-- The staging_cleanup_queue is created FIRST, in this same migration, so the enqueueing trigger installed
-- below never references a not-yet-created table and there is no migration window in which a terminal
-- transition clears the attempt identity without recording the cleanup (the abandoned staging/candidate
-- objects would otherwise leak). The trigger enqueues, so it must never predate its queue.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql (trigger function body must be byte-identical).

-- Durable retry queue for deleting superseded/abandoned freeze STAGING and published CANDIDATE objects. S3
-- deletion is not transactional with Postgres, so the delete is enqueued (idempotently) in the same
-- transaction that makes the object garbage -- completion commit, stale-recovery reset, or this terminal
-- trigger -- and a worker drains it with token-fenced leases and retries. This replaces the previous
-- best-effort, one-shot deletes that leaked storage on any failure/crash.
CREATE TABLE IF NOT EXISTS foghorn.staging_cleanup_queue (
    object_key       TEXT PRIMARY KEY,
    enqueued_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until     TIMESTAMPTZ,
    lease_token      TEXT,
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT
);
CREATE INDEX IF NOT EXISTS idx_foghorn_staging_cleanup_due ON foghorn.staging_cleanup_queue(next_attempt_at);

CREATE OR REPLACE FUNCTION foghorn.clear_sync_identity_on_terminal() RETURNS trigger AS $$
BEGIN
    -- Before clearing the attempt identity, durably enqueue the abandoned attempt's STAGING objects AND its
    -- published CANDIDATE (+ co-located .dtsh) for cleanup: once the request ids are NULL, purge can no longer
    -- DERIVE these keys, so a PUT/promote that landed for the abandoned attempt would leak. The MAIN attempt
    -- enqueues all FOUR keys it can produce (a main upload can BUNDLE a .dtsh, staged at <k>.dtsh.staging.<req>
    -- and promoted to <k>.dtsh.att-<req>), mirroring control.applySyncCompletionFailure's enqueue set:
    -- staging <k>.staging.<req>; .dtsh staging <k>.dtsh.staging.<req>; media candidate <k>.att-<req>;
    -- .dtsh candidate <k>.dtsh.att-<req>.
    IF OLD.sync_object_key IS NOT NULL AND OLD.sync_object_key <> '' THEN
        IF OLD.sync_request_id IS NOT NULL AND OLD.sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key) VALUES
                (OLD.sync_object_key || '.staging.' || OLD.sync_request_id),
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.sync_request_id),
                (OLD.sync_object_key || '.att-' || OLD.sync_request_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.sync_request_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
        IF OLD.dtsh_sync_request_id IS NOT NULL AND OLD.dtsh_sync_request_id <> '' THEN
            INSERT INTO foghorn.staging_cleanup_queue (object_key) VALUES
                (OLD.sync_object_key || '.dtsh.staging.' || OLD.dtsh_sync_request_id),
                (OLD.sync_object_key || '.dtsh.att-' || OLD.dtsh_sync_request_id)
            ON CONFLICT (object_key) DO NOTHING;
        END IF;
    END IF;
    NEW.sync_request_id := NULL;
    NEW.sync_node_id := NULL;
    NEW.dtsh_sync_request_id := NULL;
    NEW.dtsh_sync_node_id := NULL;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS artifact_clear_sync_identity_on_terminal ON foghorn.artifacts;
CREATE TRIGGER artifact_clear_sync_identity_on_terminal
    BEFORE UPDATE ON foghorn.artifacts
    FOR EACH ROW
    WHEN (
        NEW.status IN ('deleted', 'expired', 'aborted')
        AND OLD.status IS DISTINCT FROM NEW.status
        AND (OLD.sync_request_id IS NOT NULL OR OLD.sync_node_id IS NOT NULL
             OR OLD.dtsh_sync_request_id IS NOT NULL OR OLD.dtsh_sync_node_id IS NOT NULL)
    )
    EXECUTE FUNCTION foghorn.clear_sync_identity_on_terminal();

-- ADD CONSTRAINT runs NOT VALID so the add is non-blocking; v0.2.96/postdeploy/001 VALIDATEs. Idempotent.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_sync_identity_paired;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_sync_identity_paired
        CHECK ((sync_request_id IS NULL) = (sync_node_id IS NULL)) NOT VALID;

ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_dtsh_identity_paired;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_dtsh_identity_paired
        CHECK ((dtsh_sync_request_id IS NULL) = (dtsh_sync_node_id IS NULL)) NOT VALID;

ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_terminal_no_identity;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_terminal_no_identity
        CHECK (
            status NOT IN ('deleted', 'expired', 'aborted')
            OR (sync_request_id IS NULL AND sync_node_id IS NULL
                AND dtsh_sync_request_id IS NULL AND dtsh_sync_node_id IS NULL)
        ) NOT VALID;

ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_active_freeze_state;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_active_freeze_state
        CHECK (
            sync_request_id IS NULL
            OR (status IS NOT DISTINCT FROM 'ready'
                AND storage_location IS NOT DISTINCT FROM 'freezing'
                AND sync_status IS NOT DISTINCT FROM 'in_progress'
                AND NULLIF(BTRIM(sync_object_key), '') IS NOT NULL)
        ) NOT VALID;

-- clip/vod ONLY: whole-DVR .dtsh sync was retired (a DVR is reclaimed segment-wise; its chapters freeze as
-- their own VOD artifacts), so a DVR row may never hold an active .dtsh identity.
ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_active_dtsh_state;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_active_dtsh_state
        CHECK (
            dtsh_sync_request_id IS NULL
            OR (dtsh_status IS NOT DISTINCT FROM 'in_progress'
                AND sync_status IS NOT DISTINCT FROM 'synced'
                AND dtsh_synced IS FALSE
                AND COALESCE(artifact_type IN ('clip', 'vod') AND status = 'ready', false))
        ) NOT VALID;
