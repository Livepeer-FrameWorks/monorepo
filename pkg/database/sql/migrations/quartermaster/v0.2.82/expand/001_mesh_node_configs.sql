CREATE TABLE IF NOT EXISTS quartermaster.mesh_topology_state (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    revision BIGINT NOT NULL DEFAULT 1,
    warmed_revision BIGINT NOT NULL DEFAULT 0,
    warmed_planner_version TEXT NOT NULL DEFAULT '',
    warming_started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE IF EXISTS quartermaster.mesh_topology_state
    ADD COLUMN IF NOT EXISTS warmed_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS warmed_planner_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS warming_started_at TIMESTAMPTZ;

INSERT INTO quartermaster.mesh_topology_state (id, revision)
VALUES (TRUE, 1)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS quartermaster.mesh_node_configs (
    node_id VARCHAR(100) PRIMARY KEY REFERENCES quartermaster.infrastructure_nodes(node_id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    mesh_revision TEXT NOT NULL,
    topology_source_hash TEXT NOT NULL,
    wireguard_ip INET NOT NULL,
    wireguard_port INTEGER NOT NULL,
    peers JSONB NOT NULL DEFAULT '[]'::jsonb,
    service_endpoints JSONB NOT NULL DEFAULT '{}'::jsonb,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_qm_mesh_node_configs_peers_array CHECK (jsonb_typeof(peers) = 'array'),
    CONSTRAINT chk_qm_mesh_node_configs_service_endpoints_object CHECK (jsonb_typeof(service_endpoints) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_qm_mesh_node_configs_cluster ON quartermaster.mesh_node_configs(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_mesh_node_configs_revision ON quartermaster.mesh_node_configs(mesh_revision);
