-- service_cluster_assignments carries the logical media-cluster identity used
-- by DNS and DiscoverServices, while service_instances.cluster_id stays bound
-- to the physical/runtime cluster.

CREATE TABLE IF NOT EXISTS quartermaster.service_cluster_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_instance_id UUID NOT NULL REFERENCES quartermaster.service_instances(id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(service_instance_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_qm_sca_cluster ON quartermaster.service_cluster_assignments(cluster_id) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_qm_sca_instance ON quartermaster.service_cluster_assignments(service_instance_id);

DO $$
BEGIN
    IF to_regclass('quartermaster.foghorn_cluster_assignments') IS NOT NULL THEN
        INSERT INTO quartermaster.service_cluster_assignments
            (service_instance_id, cluster_id, is_active, created_at, updated_at)
        SELECT foghorn_instance_id, cluster_id, is_active, created_at, NOW()
        FROM quartermaster.foghorn_cluster_assignments
        ON CONFLICT (service_instance_id, cluster_id) DO NOTHING;
    END IF;
END $$;
