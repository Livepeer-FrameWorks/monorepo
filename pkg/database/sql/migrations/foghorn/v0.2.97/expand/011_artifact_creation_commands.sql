-- Durable command ledger for artifact creation attempts, keyed by the Commodore
-- creation-intent request_id. Written 'accepted' when a create handler begins,
-- 'committed' (with the artifact's catalog_revision) atomically with the
-- foghorn.artifacts insert on success, 'rejected' only on a definitive rejection.
-- GetArtifactCreationStatus reads this ledger so Commodore's convergence sweep
-- resolves a lost/ambiguous create by the attempt's OWN recorded outcome rather
-- than inferring rejection from artifact-row absence.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same table in the
-- baseline so a fresh init and an upgrade converge.
-- The state machine is closed by CHECK constraints: kind is a fixed artifact
-- discriminator and status is 'accepted' -> 'committed' | 'rejected' (terminal), so a
-- typo or an out-of-domain write is rejected at the database rather than silently
-- stored.
CREATE TABLE IF NOT EXISTS foghorn.artifact_creation_commands (
    request_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'accepted',
    catalog_revision BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT artifact_creation_commands_kind_check CHECK (kind IN ('clip', 'dvr', 'vod')),
    CONSTRAINT artifact_creation_commands_status_check CHECK (status IN ('accepted', 'committed', 'rejected'))
);
