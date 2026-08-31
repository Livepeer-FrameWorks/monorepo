ALTER TABLE foghorn.artifact_nodes
    ADD COLUMN IF NOT EXISTS inventory_reported_at_ms BIGINT NOT NULL DEFAULT 0;

ALTER TABLE foghorn.artifact_nodes
    DROP CONSTRAINT IF EXISTS chk_artifact_nodes_inventory_reported_at_ms;

ALTER TABLE foghorn.artifact_nodes
    ADD CONSTRAINT chk_artifact_nodes_inventory_reported_at_ms
    CHECK (inventory_reported_at_ms >= 0) NOT VALID;
