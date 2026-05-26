ALTER TABLE quartermaster.infrastructure_clusters
    ADD COLUMN IF NOT EXISTS public_topology BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_qm_clusters_public_topology
    ON quartermaster.infrastructure_clusters(public_topology)
    WHERE public_topology = true;
