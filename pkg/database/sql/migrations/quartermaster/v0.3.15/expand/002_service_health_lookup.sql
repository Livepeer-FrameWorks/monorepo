CREATE INDEX IF NOT EXISTS idx_qm_service_instances_service_health
    ON quartermaster.service_instances(service_id ASC, instance_id ASC)
    INCLUDE (cluster_id, protocol, advertise_host, port, health_endpoint_override,
             health_status, last_health_check);
