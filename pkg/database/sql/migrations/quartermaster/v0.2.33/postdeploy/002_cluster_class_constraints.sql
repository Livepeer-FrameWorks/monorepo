-- Enforce the value sets the application writes for cluster_class and
-- reassignment_state. NOT VALID first so the ALTER doesn't take a table
-- scan; VALIDATE runs in the next statement to confirm existing rows
-- comply (pre-launch all rows are either NULL or set by reconciler-side
-- code that already produces the same value set).

ALTER TABLE quartermaster.infrastructure_clusters
    ADD CONSTRAINT chk_cluster_class
    CHECK (cluster_class IS NULL OR cluster_class IN (
        'platform_official', 'tenant_private', 'third_party_marketplace'
    )) NOT VALID;

ALTER TABLE quartermaster.infrastructure_clusters
    VALIDATE CONSTRAINT chk_cluster_class;

ALTER TABLE quartermaster.infrastructure_clusters
    ADD CONSTRAINT chk_cluster_reassignment_state
    CHECK (reassignment_state IS NULL OR reassignment_state IN ('draining')) NOT VALID;

ALTER TABLE quartermaster.infrastructure_clusters
    VALIDATE CONSTRAINT chk_cluster_reassignment_state;
