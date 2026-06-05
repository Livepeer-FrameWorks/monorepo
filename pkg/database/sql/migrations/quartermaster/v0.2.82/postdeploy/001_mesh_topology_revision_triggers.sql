CREATE OR REPLACE FUNCTION quartermaster.bump_mesh_topology_state()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO quartermaster.mesh_topology_state (id, revision, updated_at)
    VALUES (TRUE, 1, NOW())
    ON CONFLICT (id)
    DO UPDATE SET revision = quartermaster.mesh_topology_state.revision + 1,
                  updated_at = NOW();

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_nodes_insert_delete ON quartermaster.infrastructure_nodes;
CREATE TRIGGER trg_qm_mesh_topology_nodes_insert_delete
AFTER INSERT OR DELETE ON quartermaster.infrastructure_nodes
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

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
    OLD.metadata IS DISTINCT FROM NEW.metadata
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_services_insert_delete ON quartermaster.services;
CREATE TRIGGER trg_qm_mesh_topology_services_insert_delete
AFTER INSERT OR DELETE ON quartermaster.services
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_services_update ON quartermaster.services;
CREATE TRIGGER trg_qm_mesh_topology_services_update
AFTER UPDATE OF type, plane ON quartermaster.services
FOR EACH ROW
WHEN (
    OLD.type IS DISTINCT FROM NEW.type OR
    OLD.plane IS DISTINCT FROM NEW.plane
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_instances_insert_delete ON quartermaster.service_instances;
CREATE TRIGGER trg_qm_mesh_topology_service_instances_insert_delete
AFTER INSERT OR DELETE ON quartermaster.service_instances
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_instances_update ON quartermaster.service_instances;
CREATE TRIGGER trg_qm_mesh_topology_service_instances_update
AFTER UPDATE OF cluster_id, node_id, service_id, status, metadata ON quartermaster.service_instances
FOR EACH ROW
WHEN (
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.node_id IS DISTINCT FROM NEW.node_id OR
    OLD.service_id IS DISTINCT FROM NEW.service_id OR
    OLD.status IS DISTINCT FROM NEW.status OR
    OLD.metadata IS DISTINCT FROM NEW.metadata
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments ON quartermaster.service_cluster_assignments;
DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments_insert_delete ON quartermaster.service_cluster_assignments;
CREATE TRIGGER trg_qm_mesh_topology_service_assignments_insert_delete
AFTER INSERT OR DELETE ON quartermaster.service_cluster_assignments
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments_update ON quartermaster.service_cluster_assignments;
CREATE TRIGGER trg_qm_mesh_topology_service_assignments_update
AFTER UPDATE OF service_instance_id, cluster_id, is_active ON quartermaster.service_cluster_assignments
FOR EACH ROW
WHEN (
    OLD.service_instance_id IS DISTINCT FROM NEW.service_instance_id OR
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.is_active IS DISTINCT FROM NEW.is_active
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();
