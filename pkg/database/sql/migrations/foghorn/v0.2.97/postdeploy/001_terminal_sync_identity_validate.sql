-- Validate the freeze-attempt-identity CHECK constraints added NOT VALID in
-- v0.2.97/expand/016_terminal_sync_identity_contract.sql (now that the trigger clears identity on every
-- terminal transition and all writers set request/node together). VALIDATE CONSTRAINT scans existing rows
-- under a SHARE UPDATE EXCLUSIVE lock; non-blocking for SELECT/INSERT/UPDATE/DELETE. Idempotent.
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT chk_foghorn_artifacts_sync_identity_paired;
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT chk_foghorn_artifacts_dtsh_identity_paired;
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT chk_foghorn_artifacts_terminal_no_identity;
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT chk_foghorn_artifacts_active_freeze_state;
ALTER TABLE foghorn.artifacts
    VALIDATE CONSTRAINT chk_foghorn_artifacts_active_dtsh_state;
