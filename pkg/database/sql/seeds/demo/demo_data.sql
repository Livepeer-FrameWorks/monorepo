-- Demo seed for Quartermaster and Purser

-- Ensure base cluster (platform default with marketplace fields)
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_cluster,
    visibility, pricing_model, short_description
)
VALUES (
    'central-primary', 'Central Primary Cluster', 'central', 'demo.frameworks.network',
    10000, 1000000, 100000,
    TRUE, TRUE,  -- Default cluster for auto-subscription, platform-operated
    'public', 'free_unmetered', 'FrameWorks shared infrastructure for all users'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    is_default_cluster = TRUE,
    is_platform_cluster = TRUE,
    visibility = 'public',
    pricing_model = 'free_unmetered',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

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

-- Demo API token for programmatic access testing
-- Token value format: "fw_" + 64 hex chars (matching developer_tokens package format)
INSERT INTO commodore.api_tokens (
    id, tenant_id, user_id, token_value, token_name,
    permissions, is_active, expires_at, last_used_at, created_at
) VALUES (
    'a0000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    '550e8400-e29b-41d4-a716-446655440000',
    'fw_demo0000000000000000000000000000000000000000000000000000000000',
    'Demo API Token',
    ARRAY['streams:read', 'streams:write', 'analytics:read'],
    TRUE,
    NOW() + INTERVAL '1 year',
    NOW() - INTERVAL '1 hour',
    NOW() - INTERVAL '7 days'
) ON CONFLICT (token_value) DO NOTHING;

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

-- Cluster pricing for the platform cluster (free tier, no metering)
INSERT INTO purser.cluster_pricing (
    cluster_id, pricing_model,
    is_platform_official, allow_free_tier, required_tier_level,
    default_quotas
) VALUES (
    'central-primary', 'free_unmetered',
    TRUE, TRUE, 0,  -- Platform cluster, available to free tier
    '{"max_streams": 5, "max_viewers": 500, "max_bandwidth_mbps": 100, "retention_days": 7}'
) ON CONFLICT (cluster_id) DO UPDATE SET
    pricing_model = 'free_unmetered',
    is_platform_official = TRUE,
    allow_free_tier = TRUE;

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

-- Demo bootstrap token for node provisioning testing
-- This token was used to provision edge-node-1
INSERT INTO quartermaster.bootstrap_tokens (
    id, token, kind, name,
    tenant_id, cluster_id, expected_ip,
    metadata, usage_limit, usage_count,
    expires_at, used_at, created_by, created_at
) VALUES (
    'b0000000-0000-0000-0000-000000000001',
    'demo_bootstrap_token_for_local_development_testing_only',
    'edge_node',
    'Demo Edge Node Bootstrap',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'central-primary',                        -- Central cluster
    '127.0.0.1',                              -- Expected localhost for dev
    '{"purpose": "demo", "environment": "development"}',
    10,    -- Max 10 uses
    1,     -- Already used once for edge-node-1
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '1 day',                 -- Used yesterday
    '550e8400-e29b-41d4-a716-446655440000',  -- Created by demo user
    NOW() - INTERVAL '2 days'
) ON CONFLICT (token) DO UPDATE SET
    expires_at = NOW() + INTERVAL '30 days';

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
       "avg_bitrate": 4500,
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
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Total Viewers - current month (REQUIRED for UsageSummary.totalViewers)
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'total_viewers', 4936,
     '{"sessions": 4936, "returning": 1247}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Viewer Hours - current month (REQUIRED for UsageSummary.viewerHours)
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'viewer_hours', 6543,
     '{"avg_session_minutes": 79.5}',
     TO_CHAR(NOW(), 'YYYY-MM'), DATE_TRUNC('month', NOW()), NOW()),
    -- Unique Viewers - current month (REQUIRED for UsageSummary.uniqueViewers)
    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'unique_viewers', 5000,
     '{"by_country": {"US": 1500, "NL": 1000, "GB": 500, "DE": 500, "JP": 500, "other": 1000}}',
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
     	    DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'egress_gb', 1245.6,
     '{"viewer_sessions": 35000, "avg_quality": "1080p"}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     	    DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'storage_gb', 72.5,
     '{"dvr_gb": 38.1, "clips_gb": 10.2, "recordings_gb": 24.2}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     	    DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'total_streams', 8,
     '{"live_streams": 5, "vod_streams": 3}',
     TO_CHAR(NOW() - INTERVAL '1 month', 'YYYY-MM'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     	    DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),    ('00000000-0000-0000-0000-000000000001', 'central-primary', 'peak_viewers', 145,
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

-- ============================================================================
-- PURSER: Demo Billing Invoices (Historical Invoice Records)
-- ============================================================================
-- Invoice history for billing page display
-- Note: status values are 'pending', 'paid', 'overdue', 'cancelled'

INSERT INTO purser.billing_invoices (
    id, tenant_id, status, currency, amount,
    due_date, paid_at,
    base_amount, metered_amount, usage_details,
    created_at
) VALUES
-- Current month (pending invoice)
(
    'fa000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    'pending',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month' + INTERVAL '14 days',
    NULL,
    249.00,  -- Developer tier base
    0.00,    -- No metered charges yet
    '{"usage_data": {"viewer_hours": 4166.67, "average_storage_gb": 23.5, "gpu_hours": 8.2, "stream_hours": 127.5, "egress_gb": 456.78}, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW())
),
-- Previous month (paid invoice)
(
    'fa000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'paid',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW()) + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    249.00,
    0.00,
    '{"usage_data": {"viewer_hours": 7500.0, "average_storage_gb": 45.2, "gpu_hours": 15.5, "stream_hours": 342.0, "egress_gb": 1245.6}, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '1 month')
),
-- Two months ago (paid invoice)
(
    'fa000000-0000-0000-0000-000000000003',
    '00000000-0000-0000-0000-000000000001',
    'paid',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    249.00,
    0.00,
    '{"usage_data": {"viewer_hours": 5833.33, "average_storage_gb": 32.1, "gpu_hours": 12.0, "stream_hours": 215.25, "egress_gb": 890.2}, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '2 months')
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    paid_at = EXCLUDED.paid_at;

-- ============================================================================
-- COMMODORE: Demo Clips (Business Registry)
-- ============================================================================
-- Clip business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.clips (
    id, tenant_id, user_id, stream_id, clip_hash,
    title, description,
    start_time, duration, clip_mode,
    retention_until, created_at, updated_at
) VALUES
-- Demo clip (ready) - matches foghorn.artifacts entry
(
    'c1000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    '550e8400-e29b-41d4-a716-446655440000',  -- Demo user
    '00000000-0000-0000-0000-000000000002',  -- Demo stream
    'a1b2c3d4e5f6789012345678901234ab',      -- Must match foghorn.artifacts
    'Demo Highlight Reel',
    'Amazing gameplay highlights from the demo stream',
    1640995200000,  -- Unix timestamp (ms): Jan 1, 2022 00:00:00 UTC
    600000,         -- Duration (ms): 10 minutes
    'absolute',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '2 hours',
    NOW() - INTERVAL '2 hours'
),
-- Demo clip (deleted) - matches foghorn.artifacts entry
(
    'c1000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    '550e8400-e29b-41d4-a716-446655440000',
    '00000000-0000-0000-0000-000000000002',
    'b2c3d4e5f6789012345678901234bcde',      -- Must match foghorn.artifacts
    'Old Demo Clip',
    'This clip was deleted',
    1641081600000,  -- Jan 2, 2022 00:00:00 UTC
    300000,         -- 5 minutes
    'absolute',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (clip_hash) DO UPDATE SET
    title = EXCLUDED.title,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo DVR Recordings (Business Registry)
-- ============================================================================
-- DVR recording business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.dvr_recordings (
    id, tenant_id, user_id, stream_id, dvr_hash,
    internal_name,
    retention_until, created_at, updated_at
) VALUES
-- Demo DVR recording (completed) - matches foghorn.artifacts entry
(
    'd1000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    '550e8400-e29b-41d4-a716-446655440000',  -- Demo user
    '00000000-0000-0000-0000-000000000002',  -- Demo stream
    'fedcba98765432109876543210fedcba',      -- Must match foghorn.artifacts
    'demo_live_stream_001',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted) - matches foghorn.artifacts entry
(
    'd1000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    '550e8400-e29b-41d4-a716-446655440000',
    '00000000-0000-0000-0000-000000000002',
    'gedcba98765432109876543210fedcbb',      -- Must match foghorn.artifacts
    'demo_live_stream_001',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (dvr_hash) DO UPDATE SET
    internal_name = EXCLUDED.internal_name,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: Demo Artifacts (Unified Clip/DVR Lifecycle Table)
-- ============================================================================
-- Demo artifacts for development and testing
-- Note: Business metadata (tenant_id, title, description) is in Commodore
-- Foghorn only stores lifecycle state here
-- The artifact_hash values MUST match the clip_hash/dvr_hash in commodore.clips/dvr_recordings above

INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, internal_name, tenant_id,
    status, size_bytes, manifest_path,
    storage_location, sync_status, retention_until,
    created_at, updated_at
) VALUES
-- Demo clip (ready)
(
    'a1b2c3d4e5f6789012345678901234ab',      -- 32-char hex (must match filename)
    'clip',
    'demo_live_stream_001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant (denormalized for fallback)
    'ready',
    140795,         -- Actual file size: ~137KB
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '2 hours',
    NOW() - INTERVAL '2 hours'
),
-- Demo clip (deleted, for testing cleanup flows)
(
    'b2c3d4e5f6789012345678901234bcde',      -- fake hash
    'clip',
    'demo_live_stream_001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'deleted',
    140795,
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/b2c3d4e5f6789012345678901234bcde.mp4',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR recording (completed)
(
    'fedcba98765432109876543210fedcba',      -- 32-char hex (must match filename)
    'dvr',
    'demo_live_stream_001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'completed',
    513176,         -- Actual total size: ~501KB (2 segments + manifest)
    '/var/lib/mistserver/recordings/dvr/demo_live_stream_001/fedcba98765432109876543210fedcba.m3u8',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted, for testing cleanup flows)
(
    'gedcba98765432109876543210fedcbb',
    'dvr',
    'demo_live_stream_001',
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'deleted',
    1024000,
    '/var/lib/mistserver/recordings/dvr/demo_live_stream_001/gedcba98765432109876543210fedcbb.m3u8',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD asset (ready, warmed to edge)
(
    'c3d4e5f678901234567890123456abcd',      -- 32-char hex
    'vod',
    NULL,                                     -- No stream association
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'ready',
    52428800,        -- 50MB
    NULL,            -- No manifest for VOD (direct file playback)
    's3',            -- Stored in S3
    'synced',
    NOW() + INTERVAL '30 days',   -- 30-day retention for VOD
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD asset (processing, just uploaded)
(
    'd4e5f6789012345678901234567abcde',
    'vod',
    NULL,
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'processing',
    104857600,       -- 100MB
    NULL,
    's3',
    'synced',
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD asset (failed validation)
(
    'e5f678901234567890123456789abcdf',
    'vod',
    NULL,
    '00000000-0000-0000-0000-000000000001',  -- Demo tenant
    'failed',
    15728640,        -- 15MB
    NULL,
    's3',
    'synced',
    NOW() - INTERVAL '1 day',    -- Already expired
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    status = EXCLUDED.status,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: VOD Metadata (User-Uploaded Video Details)
-- ============================================================================
-- VOD-specific metadata like title, description, codecs, duration

INSERT INTO foghorn.vod_metadata (
    artifact_hash, filename, title, description, content_type,
    s3_upload_id, s3_key,
    duration_ms, resolution, video_codec, audio_codec, bitrate_kbps,
    width, height, fps, audio_channels, audio_sample_rate,
    created_at, updated_at
) VALUES
-- Demo VOD (ready) - Product demo video
(
    'c3d4e5f678901234567890123456abcd',
    'product_demo_2024.mp4',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'video/mp4',
    NULL,            -- Upload completed
    'vod/00000000-0000-0000-0000-000000000001/c3d4e5f678901234567890123456abcd/product_demo_2024.mp4',
    180000,          -- 3 minutes
    '1920x1080',
    'h264',
    'aac',
    5000,            -- 5 Mbps
    1920, 1080, 30.0, 2, 48000,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD (processing) - Still being validated
(
    'd4e5f6789012345678901234567abcde',
    'webinar_recording.mp4',
    'Live Streaming Webinar',
    'Educational webinar about low-latency streaming',
    'video/mp4',
    'abc123multipartupload',   -- Still has upload ID (not yet cleaned)
    'vod/00000000-0000-0000-0000-000000000001/d4e5f6789012345678901234567abcde/webinar_recording.mp4',
    NULL,            -- Not yet validated
    NULL,
    NULL,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL,
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD (failed) - Invalid format
(
    'e5f678901234567890123456789abcdf',
    'corrupted_file.avi',
    'Failed Upload',
    'This file failed validation due to unsupported format',
    'video/x-msvideo',
    NULL,
    'vod/00000000-0000-0000-0000-000000000001/e5f678901234567890123456789abcdf/corrupted_file.avi',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    title = EXCLUDED.title,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: Artifact Nodes (Warm Storage Distribution)
-- ============================================================================
-- Register demo artifacts on nodes so Foghorn can resolve them for VOD playback

INSERT INTO foghorn.artifact_nodes (
    artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned
) VALUES
-- Demo clip on edge-node-1
(
    'a1b2c3d4e5f6789012345678901234ab',
    'edge-node-1',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    140795,
    NOW(),
    false
),
-- Demo DVR on edge-node-1
(
    'fedcba98765432109876543210fedcba',
    'edge-node-1',
    '/var/lib/mistserver/recordings/dvr/demo_live_stream_001/fedcba98765432109876543210fedcba.m3u8',
    513176,
    NOW(),
    false
),
-- Demo VOD on edge-node-1 (warmed from S3)
(
    'c3d4e5f678901234567890123456abcd',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    52428800,
    NOW(),
    false
)
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    last_seen_at = NOW(),
    is_orphaned = false;

-- ============================================================================
-- PERISCOPE: Billing Cursors (Analytics Aggregation Checkpoints)
-- ============================================================================
-- Tracks last-processed timestamp for billing aggregation jobs
-- Used by Periscope Query service to resume aggregation from checkpoints

INSERT INTO periscope.billing_cursors (
    tenant_id, last_processed_at, updated_at
) VALUES (
    '00000000-0000-0000-0000-000000000001',
    NOW() - INTERVAL '1 hour',  -- Last processed 1 hour ago
    NOW()
) ON CONFLICT (tenant_id) DO UPDATE SET
    last_processed_at = NOW() - INTERVAL '1 hour',
    updated_at = NOW();