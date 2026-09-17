-- v0.3.5: persist operator-initiated control-cell reassignment of
-- tenant-private clusters and the Foghorn cell that most recently observed
-- each node.

ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS previous_control_cell_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS reassignment_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS reassignment_deadline_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS reassignment_error TEXT;

-- No shipped code wrote the former 'draining' value, so replacing the allowed
-- set cannot strand an existing row.
ALTER TABLE quartermaster.infrastructure_clusters
    DROP CONSTRAINT IF EXISTS chk_cluster_reassignment_state;

ALTER TABLE quartermaster.infrastructure_clusters
    DROP CONSTRAINT IF EXISTS chk_cluster_control_cell_reassignment_state;

ALTER TABLE quartermaster.infrastructure_clusters
    ADD CONSTRAINT chk_cluster_control_cell_reassignment_state
        CHECK (reassignment_state IS NULL OR reassignment_state IN ('switching', 'failed')) NOT VALID;

ALTER TABLE quartermaster.infrastructure_nodes
    ADD COLUMN IF NOT EXISTS control_cell_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS control_cell_observed_at TIMESTAMPTZ;
