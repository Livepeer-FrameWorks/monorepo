DROP TRIGGER IF EXISTS trg_qm_mesh_topology_nodes_update ON quartermaster.infrastructure_nodes;
CREATE TRIGGER trg_qm_mesh_topology_nodes_update
AFTER UPDATE OF cluster_id, node_name, node_type, status, internal_ip, external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port, metadata ON quartermaster.infrastructure_nodes
FOR EACH ROW
WHEN (
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.node_name IS DISTINCT FROM NEW.node_name OR
    OLD.node_type IS DISTINCT FROM NEW.node_type OR
    OLD.status IS DISTINCT FROM NEW.status OR
    OLD.internal_ip IS DISTINCT FROM NEW.internal_ip OR
    OLD.external_ip IS DISTINCT FROM NEW.external_ip OR
    OLD.wireguard_ip IS DISTINCT FROM NEW.wireguard_ip OR
    OLD.wireguard_public_key IS DISTINCT FROM NEW.wireguard_public_key OR
    OLD.wireguard_listen_port IS DISTINCT FROM NEW.wireguard_listen_port OR
    OLD.metadata->'desired_service_types' IS DISTINCT FROM NEW.metadata->'desired_service_types' OR
    OLD.metadata->'service_types' IS DISTINCT FROM NEW.metadata->'service_types' OR
    OLD.metadata->'desired_cluster_ids' IS DISTINCT FROM NEW.metadata->'desired_cluster_ids' OR
    OLD.metadata->'service_cluster_ids' IS DISTINCT FROM NEW.metadata->'service_cluster_ids' OR
    OLD.metadata->'logical_cluster_ids' IS DISTINCT FROM NEW.metadata->'logical_cluster_ids'
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_instances_update ON quartermaster.service_instances;
CREATE TRIGGER trg_qm_mesh_topology_service_instances_update
AFTER UPDATE OF cluster_id, node_id, service_id, status, metadata ON quartermaster.service_instances
FOR EACH ROW
WHEN (
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.node_id IS DISTINCT FROM NEW.node_id OR
    OLD.service_id IS DISTINCT FROM NEW.service_id OR
    OLD.status IS DISTINCT FROM NEW.status OR
    OLD.metadata->'infra_role' IS DISTINCT FROM NEW.metadata->'infra_role' OR
    OLD.metadata->'infra_name' IS DISTINCT FROM NEW.metadata->'infra_name'
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();
