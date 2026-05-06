-- Adds the latest-resource-snapshot columns Privateer writes via SyncMesh.
-- Bridge reads these for InfrastructureNode.liveState on core nodes. snapshot_at
-- is Quartermaster receipt time so freshness does not depend on node clock skew.
-- All columns are NULL until the agent on a node ships a complete snapshot —
-- old Privateer clients keep working, the columns simply stay NULL.

ALTER TABLE quartermaster.infrastructure_nodes
    ADD COLUMN IF NOT EXISTS snapshot_cpu_percent REAL,
    ADD COLUMN IF NOT EXISTS snapshot_ram_used_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS snapshot_ram_total_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS snapshot_disk_used_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS snapshot_disk_total_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS snapshot_uptime_seconds BIGINT,
    ADD COLUMN IF NOT EXISTS snapshot_at TIMESTAMPTZ;
