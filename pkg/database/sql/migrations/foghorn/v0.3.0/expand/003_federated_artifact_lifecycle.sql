-- Federation adoption creates metadata-only routing pointers. Existing active
-- pointers are normalized by the resumable
-- foghorn_federated_artifact_lifecycle_v0_3_0 data migration.
ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS federated_pointer BOOLEAN NOT NULL DEFAULT false;
