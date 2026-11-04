-- A lease's node execution survives a lost reply before cell-side preparation.
ALTER TABLE commodore.streams ADD COLUMN IF NOT EXISTS active_ingest_node_id VARCHAR(100);
ALTER TABLE commodore.streams ADD COLUMN IF NOT EXISTS active_ingest_control_cell_id VARCHAR(255);
CREATE INDEX IF NOT EXISTS idx_publisher_lease_execution ON commodore.streams
    (active_ingest_control_cell_id, active_ingest_node_id, active_ingest_claim_id)
    WHERE active_ingest_claim_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS commodore.publisher_lease_abandonments (
    control_cell_id VARCHAR(255) NOT NULL CHECK (btrim(control_cell_id) <> ''),
    node_id VARCHAR(100) NOT NULL CHECK (btrim(node_id) <> ''),
    execution_token VARCHAR(64) NOT NULL CHECK (btrim(execution_token) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (control_cell_id, node_id, execution_token)
);
CREATE INDEX IF NOT EXISTS idx_publisher_lease_abandonments_created_range
    ON commodore.publisher_lease_abandonments(created_at ASC);
