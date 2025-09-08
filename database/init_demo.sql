-- Demo seed for Quartermaster and Purser

-- Ensure base cluster
INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps)
VALUES ('central-primary', 'Central Primary Cluster', 'central', 'demo.frameworks.network', 10000, 1000000, 100000)
ON CONFLICT (cluster_id) DO NOTHING;

-- Ensure service catalog minimal entry
INSERT INTO quartermaster.services (service_id, name, plane, description, default_port, health_check_path, docker_image)
VALUES ('api_tenants', 'Quartermaster', 'control', 'Tenant and cluster management service', 9008, '/health', 'frameworks/quartermaster')
ON CONFLICT (service_id) DO NOTHING;

-- Assign service to cluster
INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas, config_blob)
VALUES ('central-primary', 'api_tenants', 'running', 1, '{"database_url": "postgres://frameworks_user:frameworks_dev@postgres:5432/frameworks"}')
ON CONFLICT (cluster_id, service_id) DO NOTHING;

-- Demo tenant
INSERT INTO quartermaster.tenants (id, name, subdomain, plan, max_streams, max_users, primary_cluster_id)
VALUES ('00000000-0000-0000-0000-000000000001', 'Demo Organization', 'demo', 'pro', 50, 10, 'central-primary')
ON CONFLICT (id) DO NOTHING;

-- Demo user
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions)
VALUES ('550e8400-e29b-41d4-a716-446655440000', '00000000-0000-0000-0000-000000000001', 'demo@frameworks.dev', '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla', 'Demo', 'User', 'owner', ARRAY['streams:read','streams:write','analytics:read','users:read','users:write','settings:write'])
ON CONFLICT (tenant_id, email) DO NOTHING;

UPDATE commodore.users SET verified = TRUE WHERE email = 'demo@frameworks.dev' AND tenant_id = '00000000-0000-0000-0000-000000000001';

-- Service account
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified)
VALUES ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001', 'service@internal', 'no-login', 'Service', 'Account', 'service', ARRAY['*'], TRUE, TRUE)
ON CONFLICT (tenant_id, email) DO NOTHING;

-- Create demo stream via function
DO $$
DECLARE
    demo_tenant_id UUID := '00000000-0000-0000-0000-000000000001';
    demo_user_id UUID := '550e8400-e29b-41d4-a716-446655440000';
    stream_result RECORD;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM commodore.streams WHERE tenant_id = demo_tenant_id AND user_id = demo_user_id) THEN
        SELECT * INTO stream_result FROM commodore.create_user_stream(demo_tenant_id, demo_user_id, 'Demo Stream');
    END IF;
END $$;

-- Ensure cluster is owned by demo tenant to allow fingerprint-based association
UPDATE quartermaster.infrastructure_clusters
SET owner_tenant_id = '00000000-0000-0000-0000-000000000001'
WHERE cluster_id = 'central-primary';

-- Pre-provision a demo infrastructure node that matches the default NODE_NAME in docker-compose
INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, health_score, tags, metadata)
VALUES ('edge-node-1', 'central-primary', 'edge-node-1', 'edge', 'active', 1.0, '{}', '{}')
ON CONFLICT (node_id) DO NOTHING;

-- Demo subscription in Purser
INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_email, started_at, next_billing_date)
SELECT '00000000-0000-0000-0000-000000000001', bt.id, 'active', 'demo@frameworks.dev', NOW(), NOW() + INTERVAL '1 month'
FROM purser.billing_tiers bt 
WHERE bt.tier_name = 'developer'
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = '00000000-0000-0000-0000-000000000001');

-- Bind Helmsman demo node fingerprint (machine-id SHA-256) to demo tenant for immediate matching
-- machine-id contents: frameworks-demo-helmsman
-- sha256: 3d0800fc0eb588967e6c6e03228815bbb59559107890b4799cc563a69f2f9d03
INSERT INTO quartermaster.node_fingerprints (
    tenant_id,
    node_id,
    fingerprint_machine_sha256,
    fingerprint_macs_sha256,
    seen_ips,
    attrs
) VALUES (
    '00000000-0000-0000-0000-000000000001',
    'edge-node-1',
    '3d0800fc0eb588967e6c6e03228815bbb59559107890b4799cc563a69f2f9d03',
    NULL,
    '{}',
    '{}'
) ON CONFLICT (node_id) DO UPDATE SET
    fingerprint_machine_sha256 = EXCLUDED.fingerprint_machine_sha256,
    last_seen = NOW();
