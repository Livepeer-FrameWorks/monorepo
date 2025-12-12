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
INSERT INTO quartermaster.tenants (id, name, subdomain, deployment_tier, primary_cluster_id)
VALUES ('00000000-0000-0000-0000-000000000001', 'Demo Organization', 'demo', 'pro', 'central-primary')
ON CONFLICT (id) DO NOTHING;

INSERT INTO quartermaster.tenant_cluster_assignments (tenant_id, cluster_id, deployment_tier, is_primary)
VALUES ('00000000-0000-0000-0000-000000000001', 'central-primary', 'pro', TRUE)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

-- Demo user
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions)
VALUES ('550e8400-e29b-41d4-a716-446655440000', '00000000-0000-0000-0000-000000000001', 'demo@frameworks.dev', '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla', 'Demo', 'User', 'owner', ARRAY['streams:read','streams:write','analytics:read','users:read','users:write','settings:write'])
ON CONFLICT (tenant_id, email) DO NOTHING;

UPDATE commodore.users SET verified = TRUE WHERE email = 'demo@frameworks.dev' AND tenant_id = '00000000-0000-0000-0000-000000000001';

-- Service account
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified)
VALUES ('00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000001', 'service@internal', 'no-login', 'Service', 'Account', 'service', ARRAY['*'], TRUE, TRUE)
ON CONFLICT (tenant_id, email) DO NOTHING;

-- Create demo stream with fixed internal_name to match ClickHouse seed data
INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, description)
VALUES (
    '00000000-0000-0000-0000-000000000002',  -- Fixed demo stream UUID
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    '550e8400-e29b-41d4-a716-446655440000',  -- Demo user
    'sk_demo_live_stream_primary_key',       -- Fixed stream key
    'pb_demo_live_001',                      -- Fixed playback ID
    'demo_live_stream_001',                  -- MUST match ClickHouse seed data
    'Demo Stream',
    'Demo stream for development and testing'
) ON CONFLICT (internal_name) DO NOTHING;

-- Create primary stream key for demo stream
INSERT INTO commodore.stream_keys (tenant_id, user_id, stream_id, key_value, key_name, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    '550e8400-e29b-41d4-a716-446655440000',
    '00000000-0000-0000-0000-000000000002',
    'sk_demo_live_stream_primary_key',
    'Primary Key',
    TRUE
) ON CONFLICT (key_value) DO NOTHING;

-- Ensure cluster is owned by demo tenant to allow fingerprint-based association
UPDATE quartermaster.infrastructure_clusters
SET owner_tenant_id = '00000000-0000-0000-0000-000000000001'
WHERE cluster_id = 'central-primary';

-- Pre-provision a demo infrastructure node that matches HELMSMAN_NODE_ID in docker-compose
-- region matches MistServer config location; IPs are localhost for local dev
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES (
    'edge-node-1', 'central-primary', 'edge-node-1', 'edge', 'active',
    'Leiden', '127.0.0.1', '127.0.0.1', 52.1601, 4.4970, '{}', '{}'
) ON CONFLICT (node_id) DO UPDATE SET
    region = EXCLUDED.region,
    external_ip = EXCLUDED.external_ip,
    internal_ip = EXCLUDED.internal_ip,
    latitude = EXCLUDED.latitude,
    longitude = EXCLUDED.longitude;

-- Demo subscription in Purser
INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_email, started_at, next_billing_date)
SELECT '00000000-0000-0000-0000-000000000001', bt.id, 'active', 'demo@frameworks.dev', NOW(), NOW() + INTERVAL '1 month'
FROM purser.billing_tiers bt 
WHERE bt.tier_name = 'developer'
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = '00000000-0000-0000-0000-000000000001');

-- Grant access (subscription) to the central cluster for the demo tenant
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, is_active
) VALUES (
    '00000000-0000-0000-0000-000000000001', 'central-primary', 'owner', TRUE
) ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    is_active = TRUE;

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

-- Demo usage records for billing page
-- Current month usage (ongoing)
-- NOTE: usage_details must include all fields expected by UsageSummary GraphQL type
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, period_start, period_end)
VALUES
    -- Stream hours - current month (includes rich usage_details for UsageSummary)
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'stream_hours', 127.5,
     '{
       "stream_count": 3,
       "avg_duration_hours": 42.5,
       "max_viewers": 89,
       "unique_users": 342,
       "avg_viewers": 38.5,
       "unique_countries": 7,
       "unique_cities": 23,
       "avg_buffer_health": 0.92,
       "avg_bitrate": 4500000,
       "packet_loss_rate": 0.0012,
       "geo_breakdown": [
         {"country_code": "US", "viewer_count": 1511, "viewer_hours": 245.3, "percentage": 30.6, "egress_gb": 142.5},
         {"country_code": "NL", "viewer_count": 993, "viewer_hours": 156.2, "percentage": 20.1, "egress_gb": 91.2},
         {"country_code": "GB", "viewer_count": 491, "viewer_hours": 78.4, "percentage": 10.0, "egress_gb": 45.8},
         {"country_code": "DE", "viewer_count": 456, "viewer_hours": 71.2, "percentage": 9.2, "egress_gb": 41.3},
         {"country_code": "JP", "viewer_count": 510, "viewer_hours": 82.1, "percentage": 10.3, "egress_gb": 48.2}
       ]
     }',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Egress GB - current month
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'egress_gb', 456.78,
     '{"viewer_sessions": 12500, "avg_quality": "1080p"}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Storage GB - current month
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'storage_gb', 89.3,
     '{"dvr_gb": 45.2, "clips_gb": 12.8, "recordings_gb": 31.3}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Total Streams - current month
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'total_streams', 3,
     '{"live_streams": 2, "vod_streams": 1}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Peak Viewers - current month
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'peak_viewers', 89,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-10-15T14:30:00Z"}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW())
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    usage_details = EXCLUDED.usage_details;

-- Previous month usage (finalized)
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, period_start, period_end)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'stream_hours', 342.0,
     '{"stream_count": 8, "avg_duration_hours": 42.75}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'egress_gb', 1245.6,
     '{"viewer_sessions": 35000, "avg_quality": "1080p"}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'storage_gb', 72.5,
     '{"dvr_gb": 38.1, "clips_gb": 10.2, "recordings_gb": 24.2}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'total_streams', 8,
     '{"live_streams": 5, "vod_streams": 3}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'peak_viewers', 145,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-09-20T18:45:00Z"}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second')
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    usage_details = EXCLUDED.usage_details;

-- Two months ago usage
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, period_start, period_end)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'stream_hours', 215.25,
     '{"stream_count": 5, "avg_duration_hours": 43.05}',
     TO_CHAR(NOW() - INTERVAL '2 months', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'egress_gb', 890.2,
     '{"viewer_sessions": 24000, "avg_quality": "720p"}',
     TO_CHAR(NOW() - INTERVAL '2 months', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'storage_gb', 58.9,
     '{"dvr_gb": 30.5, "clips_gb": 8.4, "recordings_gb": 20.0}',
     TO_CHAR(NOW() - INTERVAL '2 months', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'total_streams', 5,
     '{"live_streams": 3, "vod_streams": 2}',
     TO_CHAR(NOW() - INTERVAL '2 months', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'peak_viewers', 112,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-08-10T12:15:00Z"}',
     TO_CHAR(NOW() - INTERVAL '2 months', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second')
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    usage_details = EXCLUDED.usage_details;
