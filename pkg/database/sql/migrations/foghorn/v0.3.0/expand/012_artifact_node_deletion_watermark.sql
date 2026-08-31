CREATE TABLE IF NOT EXISTS foghorn.artifact_node_deletion_watermark (
    artifact_hash VARCHAR(32) NOT NULL REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,
    node_id VARCHAR(100) NOT NULL,
    deleted_at_ms BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (artifact_hash, node_id)
);

ALTER TABLE foghorn.artifact_node_deletion_watermark
    DROP CONSTRAINT IF EXISTS chk_artifact_node_deletion_watermark_time;

ALTER TABLE foghorn.artifact_node_deletion_watermark
    ADD CONSTRAINT chk_artifact_node_deletion_watermark_time
    CHECK (deleted_at_ms >= 0) NOT VALID;
