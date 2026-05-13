-- VirtualFoghorn assignment columns. Every tenant-private / marketplace /
-- self-hosted cluster needs an explicit regional Foghorn control cell to
-- own Helmsman ConfigSeed distribution, tenant alias TLS bundle delivery,
-- and edge apply-state ACK. For platform_official clusters the assignment
-- equals cell_id (self-control). Navigator multi-cell apply-state fanout
-- consults this when deciding which Foghorn to query before publishing
-- tenant alias DNS membership.
--
-- Column-add only. Values populated by cluster_provision (manifest-derived
-- for platform_official: control_cell_id = cell_id; explicit for other
-- classes via CreatePrivateCluster / ReassignClusterControlCell). The
-- reassignment_state CHECK lives in the schema baseline.
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS control_cell_id VARCHAR(100),
    ADD COLUMN IF NOT EXISTS eligible_serving_cell_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN IF NOT EXISTS reassignment_state VARCHAR(20);
