CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_service_instances_manifest_owner
 ON quartermaster.service_instances(registry_owner ASC, cluster_id ASC, id ASC)
 WHERE registration_source = 'manifest';
