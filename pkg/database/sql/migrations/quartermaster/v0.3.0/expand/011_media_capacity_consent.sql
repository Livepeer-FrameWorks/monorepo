ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS media_consent_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS media_allow_ingest BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS media_allow_serve BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS media_allow_external_source BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE quartermaster.infrastructure_clusters
    DROP CONSTRAINT IF EXISTS chk_cluster_media_consent_revision;
ALTER TABLE quartermaster.infrastructure_clusters
    ADD CONSTRAINT chk_cluster_media_consent_revision CHECK (media_consent_revision >= 0) NOT VALID;

CREATE TABLE IF NOT EXISTS quartermaster.media_capacity_consent_changes (
    tenant_id UUID NOT NULL,
    cluster_record_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL CHECK (btrim(idempotency_key) <> ''),
    request_sha256 BYTEA NOT NULL CHECK (octet_length(request_sha256) = 32),
    revision BIGINT NOT NULL CHECK (revision > 0),
    consent_digest VARCHAR(64) NOT NULL CHECK (consent_digest ~ '^[0-9a-f]{64}$'),
    review_digest VARCHAR(64) NOT NULL CHECK (review_digest ~ '^[0-9a-f]{64}$'),
    previous_allow_ingest BOOLEAN NOT NULL,
    previous_allow_serve BOOLEAN NOT NULL,
    previous_allow_external_source BOOLEAN NOT NULL,
    allow_ingest BOOLEAN NOT NULL,
    allow_serve BOOLEAN NOT NULL,
    allow_external_source BOOLEAN NOT NULL,
    actor_id VARCHAR(255) NOT NULL CHECK (btrim(actor_id) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, cluster_record_id, idempotency_key),
    UNIQUE (cluster_record_id, revision)
);

CREATE OR REPLACE TRIGGER trg_infrastructure_cluster_media_authority
AFTER INSERT OR DELETE OR UPDATE OF deployment_model, owner_tenant_id, cluster_class,
    cell_id, control_cell_id, eligible_serving_cell_ids, region_id, allow_private_pull_sources, is_active,
    media_consent_revision, media_allow_ingest, media_allow_serve, media_allow_external_source
ON quartermaster.infrastructure_clusters
FOR EACH ROW EXECUTE FUNCTION quartermaster.media_authority_cluster_changed();
