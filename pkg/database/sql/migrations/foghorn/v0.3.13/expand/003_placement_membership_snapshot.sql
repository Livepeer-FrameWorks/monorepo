-- The cell's last Quartermaster placement membership, so replicas keep placing
-- from it while Quartermaster is unreachable, including after a restart.
CREATE TABLE IF NOT EXISTS foghorn.placement_membership_snapshot (
    cell_id VARCHAR(100) PRIMARY KEY CHECK (btrim(cell_id) <> ''),
    revision TEXT NOT NULL CHECK (revision <> ''),
    payload BYTEA NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
