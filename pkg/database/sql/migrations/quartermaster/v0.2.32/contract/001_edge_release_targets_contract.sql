DELETE FROM quartermaster.cluster_release_targets WHERE channel = 'edge';
DELETE FROM quartermaster.edge_releases WHERE channel = 'edge';

ALTER TABLE quartermaster.edge_releases
    DROP COLUMN IF EXISTS metadata,
    ALTER COLUMN components SET NOT NULL,
    ALTER COLUMN published_at SET DEFAULT NOW(),
    DROP CONSTRAINT IF EXISTS chk_qm_edge_release_channel,
    DROP CONSTRAINT IF EXISTS chk_qm_edge_release_components_object,
    ADD CONSTRAINT chk_qm_edge_release_channel CHECK (channel IN ('stable', 'rc')),
    ADD CONSTRAINT chk_qm_edge_release_components_object CHECK (jsonb_typeof(components) = 'object');

ALTER TABLE quartermaster.cluster_release_targets
    DROP COLUMN IF EXISTS policy,
    DROP CONSTRAINT IF EXISTS chk_qm_cluster_release_target_channel,
    DROP CONSTRAINT IF EXISTS chk_qm_cluster_release_rollout_plan_object,
    ADD CONSTRAINT chk_qm_cluster_release_target_channel CHECK (channel IN ('stable', 'rc')),
    ADD CONSTRAINT chk_qm_cluster_release_rollout_plan_object CHECK (jsonb_typeof(rollout_plan) = 'object');

DROP INDEX IF EXISTS quartermaster.idx_qm_cluster_release_targets_policy;
