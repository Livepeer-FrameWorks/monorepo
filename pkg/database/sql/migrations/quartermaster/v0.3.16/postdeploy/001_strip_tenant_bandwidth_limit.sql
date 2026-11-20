UPDATE quartermaster.tenant_cluster_access
SET resource_limits = resource_limits #- '{max_bandwidth_mbps}'
WHERE resource_limits ? 'max_bandwidth_mbps';

UPDATE quartermaster.cluster_invites
SET resource_limits = resource_limits #- '{max_bandwidth_mbps}'
WHERE resource_limits ? 'max_bandwidth_mbps';
