ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS federated_purge_token UUID,
    ADD COLUMN IF NOT EXISTS federated_purge_lease_until TIMESTAMPTZ;

ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_federated_purge_pair;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_federated_purge_pair
        CHECK ((federated_purge_token IS NULL) = (federated_purge_lease_until IS NULL)) NOT VALID;

ALTER TABLE foghorn.artifacts
    DROP CONSTRAINT IF EXISTS chk_foghorn_artifacts_federated_purge_scope;
ALTER TABLE foghorn.artifacts
    ADD CONSTRAINT chk_foghorn_artifacts_federated_purge_scope
        CHECK (federated_purge_token IS NULL OR (federated_pointer = true AND status = 'deleted')) NOT VALID;
