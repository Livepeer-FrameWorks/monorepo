ALTER TABLE quartermaster.service_instances
    DROP CONSTRAINT IF EXISTS fk_qm_service_instances_node_cluster;

ALTER TABLE quartermaster.service_instances
    ADD CONSTRAINT fk_qm_service_instances_node_cluster
    FOREIGN KEY (node_id, cluster_id)
    REFERENCES quartermaster.infrastructure_nodes(node_id, cluster_id)
    DEFERRABLE INITIALLY IMMEDIATE
    NOT VALID;

ALTER TABLE quartermaster.ingress_sites
    DROP CONSTRAINT IF EXISTS fk_qm_ingress_sites_node_cluster;

ALTER TABLE quartermaster.ingress_sites
    ADD CONSTRAINT fk_qm_ingress_sites_node_cluster
    FOREIGN KEY (node_id, cluster_id)
    REFERENCES quartermaster.infrastructure_nodes(node_id, cluster_id)
    DEFERRABLE INITIALLY IMMEDIATE
    NOT VALID;
