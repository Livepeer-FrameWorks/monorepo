CREATE TABLE IF NOT EXISTS quartermaster.edge_releases (
    channel TEXT NOT NULL,
    version TEXT NOT NULL,
    components JSONB NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel, version),
    CONSTRAINT chk_qm_edge_release_channel CHECK (channel IN ('stable', 'rc')),
    CONSTRAINT chk_qm_edge_release_components_object CHECK (jsonb_typeof(components) = 'object')
);

CREATE TABLE IF NOT EXISTS quartermaster.cluster_release_targets (
    cluster_id VARCHAR(100) PRIMARY KEY REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    channel TEXT NOT NULL,
    target_version TEXT,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    rollout_plan JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_qm_cluster_release_target_channel CHECK (channel IN ('stable', 'rc')),
    CONSTRAINT chk_qm_cluster_release_rollout_plan_object CHECK (jsonb_typeof(rollout_plan) = 'object')
);

ALTER TABLE quartermaster.cluster_release_targets
    ADD COLUMN IF NOT EXISTS target_version TEXT,
    ADD COLUMN IF NOT EXISTS paused BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS rollout_plan JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_qm_edge_releases_published ON quartermaster.edge_releases(channel, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_release_targets_paused ON quartermaster.cluster_release_targets(paused);
