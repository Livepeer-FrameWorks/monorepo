-- Demo seed for Quartermaster and Purser

-- Ensure base cluster (platform default with marketplace fields)
-- Note: pricing_model is now managed via Purser, not Quartermaster
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster,
    visibility, short_description
)
VALUES (
    'central-primary', 'Central Primary Cluster', 'central', 'demo.frameworks.network',
    10000, 1000000, 100000,
    TRUE,
    'public', 'FrameWorks shared infrastructure for all users'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    is_default_cluster = TRUE,
    visibility = 'public',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

-- Ensure service catalog minimal entry
INSERT INTO quartermaster.services (service_id, name, plane, description, default_port, health_check_path, docker_image, type, protocol)
VALUES ('api_tenants', 'Quartermaster', 'control', 'Tenant and cluster management service', 9008, '/health', 'frameworks/quartermaster', 'api_tenants', 'http')
ON CONFLICT (service_id) DO NOTHING;

-- Assign service to cluster
INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas, config_blob)
VALUES ('central-primary', 'api_tenants', 'running', 1, '{"database_url": "postgres://frameworks_user:frameworks_dev@postgres:5432/frameworks"}')
ON CONFLICT (cluster_id, service_id) DO NOTHING;

-- Demo tenant
INSERT INTO quartermaster.tenants (id, name, subdomain, deployment_tier, primary_cluster_id)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'Demo Organization', 'demo', 'pro', 'central-primary')
ON CONFLICT (id) DO NOTHING;

INSERT INTO quartermaster.tenant_cluster_assignments (tenant_id, cluster_id, deployment_tier, is_primary)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'pro', TRUE)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

-- Demo user
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions)
VALUES ('5eedface-5e1f-da7a-face-5e1fda7a0001', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo@frameworks.dev', '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla', 'Demo', 'User', 'owner', ARRAY['streams:read','streams:write','analytics:read','users:read','users:write','settings:write'])
ON CONFLICT (email) DO NOTHING;

UPDATE commodore.users SET verified = TRUE WHERE email = 'demo@frameworks.dev' AND tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001';

-- Service account
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified)
VALUES ('5eeddeaf-dead-beef-deaf-deadbeef0000', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'service@internal', 'no-login', 'Service', 'Account', 'service', ARRAY['*'], TRUE, TRUE)
ON CONFLICT (email) DO NOTHING;

-- Demo API token for programmatic access testing
-- Input token format: "fw_" + 64 hex chars (matching developer_tokens package format)
-- DEMO INPUT TOKEN: fw_0000000000000000000000000000000000000000000000000000000000demo01
-- Use this token value in API requests for local development testing
INSERT INTO commodore.api_tokens (
    id, tenant_id, user_id, token_value, token_name,
    permissions, is_active, expires_at, last_used_at, created_at
) VALUES (
    '5eed5a17-da7a-5a17-da7a-5a17da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    -- SHA-256 hash of: fw_0000000000000000000000000000000000000000000000000000000000demo01
    '807a534c30fd84d3544bd6ee5f8b1c4426596a9c8c360b92caf7b667c25db8d8',
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
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Fixed demo stream UUID
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    'sk_demo_live_stream_primary_key',       -- Fixed stream key
    'pb_demo_live_001',                      -- Fixed playback ID
    'demo_live_stream_001',                  -- MUST match ClickHouse seed data
    'Demo Stream',
    'Demo stream for development and testing'
) ON CONFLICT (internal_name) DO NOTHING;

-- Create primary stream key for demo stream
INSERT INTO commodore.stream_keys (tenant_id, user_id, stream_id, key_value, key_name, is_active)
VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'sk_demo_live_stream_primary_key',
    'Primary Key',
    TRUE
) ON CONFLICT (key_value) DO NOTHING;

-- Ensure cluster is owned by demo tenant to allow fingerprint-based association
UPDATE quartermaster.infrastructure_clusters
SET owner_tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001'
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

-- Regional nodes (offline) for routing map visuals and historical data
-- These nodes are not running in docker-compose but provide geographic diversity
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES
    ('edge-leiden', 'central-primary', 'edge-leiden', 'edge', 'offline',
     'Leiden', NULL, NULL, 52.1601, 4.4970, '{"region":"eu-west"}', '{}'),
    ('edge-ashburn', 'central-primary', 'edge-ashburn', 'edge', 'offline',
     'Ashburn', NULL, NULL, 39.0438, -77.4874, '{"region":"us-east"}', '{}'),
    ('edge-singapore', 'central-primary', 'edge-singapore', 'edge', 'offline',
     'Singapore', NULL, NULL, 1.3521, 103.8198, '{"region":"apac"}', '{}')
ON CONFLICT (node_id) DO NOTHING;

-- Demo subscription in Purser
INSERT INTO purser.tenant_subscriptions (
    tenant_id, tier_id, status, billing_email, started_at, next_billing_date,
    billing_period_start, billing_period_end
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', bt.id, 'active', 'demo@frameworks.dev',
    NOW(), NOW() + INTERVAL '1 month',
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month'
FROM purser.billing_tiers bt
WHERE bt.tier_name = 'developer'
  AND NOT EXISTS (SELECT 1 FROM purser.tenant_subscriptions WHERE tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001');

-- Demo Mollie customer + mandate for the demo tenant
INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'cst_demo_123')
ON CONFLICT (tenant_id) DO UPDATE SET
    mollie_customer_id = EXCLUDED.mollie_customer_id;

INSERT INTO purser.mollie_mandates (
    tenant_id, mollie_customer_id, mollie_mandate_id,
    status, method, details, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'cst_demo_123', 'mdt_demo_123',
    'valid', 'directdebit', '{"consumer_name":"Demo User","consumer_account":"NL00DEMO0000000000"}',
    NOW() - INTERVAL '30 days', NOW()
) ON CONFLICT (mollie_mandate_id) DO UPDATE SET
    status = EXCLUDED.status,
    method = EXCLUDED.method,
    details = EXCLUDED.details,
    updated_at = NOW();

-- Demo prepaid balance for the demo tenant (starts at $50)
INSERT INTO purser.prepaid_balances (
    tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 5000, 'USD', 500, NOW() - INTERVAL '7 days', NOW()
) ON CONFLICT (tenant_id, currency) DO UPDATE SET
    balance_cents = EXCLUDED.balance_cents,
    updated_at = NOW();

-- Demo cluster subscription tracking (paid cluster flow uses this table)
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'active', NOW(), NOW()
) ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    status = EXCLUDED.status,
    updated_at = NOW();

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
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'owner', TRUE
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
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
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
    '5eedb007-5eed-da7a-b007-5eedda7a0001',
    'demo_bootstrap_token_for_local_development_testing_only',
    'edge_node',
    'Demo Edge Node Bootstrap',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'central-primary',                        -- Central cluster
    '127.0.0.1',                              -- Expected localhost for dev
    '{"purpose": "demo", "environment": "development"}',
    10,    -- Max 10 uses
    1,     -- Already used once for edge-node-1
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '1 day',                 -- Used yesterday
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Created by demo user
    NOW() - INTERVAL '2 days'
) ON CONFLICT (token) DO UPDATE SET
    expires_at = NOW() + INTERVAL '30 days';

-- ============================================================================
-- PURSER: 5-Minute Usage Records (raw data, like Periscope produces)
-- ============================================================================
-- Generate 7 days of 5-minute granularity usage records
-- 7 days * 24 hours * 12 intervals/hour = 2016 records per usage type
-- These feed the rollup job which creates daily aggregates

INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'central-primary',
    usage_type,
    base_value / 288.0 * (0.7 + 0.6 * random()),  -- Daily value / 288 5-min periods, with variance
    '{}',
    NOW() - ((n * 5) || ' minutes')::interval,
    NOW() - ((n * 5) || ' minutes')::interval + INTERVAL '5 minutes',
    'hourly'  -- 5-min records are classified as 'hourly' granularity (< 24h)
FROM generate_series(0, 2015) AS n  -- 2016 intervals (0-2015)
CROSS JOIN (VALUES
    ('stream_hours', 18.0),
    ('egress_gb', 65.0),
    ('average_storage_gb', 12.0),
    ('viewer_hours', 85.0)
) AS usage_types(usage_type, base_value)
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    granularity = EXCLUDED.granularity;

-- ============================================================================
-- PURSER: Monthly Usage Records (for billing summaries)
-- ============================================================================
-- Demo usage records for billing page
-- Current month usage (ongoing)
-- NOTE: usage_details must include all fields expected by UsageSummary GraphQL type
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity)
VALUES
    -- Stream hours - current month (includes rich usage_details for UsageSummary)
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'stream_hours', 127.5,
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
         {"country_code": "US", "viewer_count": 1511, "viewer_hours": 245.3, "egress_gb": 142.5},
         {"country_code": "NL", "viewer_count": 993, "viewer_hours": 156.2, "egress_gb": 91.2},
         {"country_code": "GB", "viewer_count": 491, "viewer_hours": 78.4, "egress_gb": 45.8},
         {"country_code": "DE", "viewer_count": 456, "viewer_hours": 71.2, "egress_gb": 41.3},
         {"country_code": "JP", "viewer_count": 510, "viewer_hours": 82.1, "egress_gb": 48.2}
       ]
     }',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Egress GB - current month
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'egress_gb', 456.78,
     '{"viewer_sessions": 12500, "avg_quality": "1080p"}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Storage GB - current month
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'average_storage_gb', 89.3,
     '{"dvr_gb": 45.2, "clips_gb": 12.8, "recordings_gb": 31.3}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Total Streams - current month
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'total_streams', 3,
     '{"live_streams": 2, "vod_streams": 1}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Peak Viewers - current month
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'peak_viewers', 89,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-10-15T14:30:00Z"}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Total Viewers - current month (REQUIRED for UsageSummary.totalViewers)
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'total_viewers', 4936,
     '{"sessions": 4936, "returning": 1247}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Viewer Hours - current month (REQUIRED for UsageSummary.viewerHours)
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'viewer_hours', 6543,
     '{"avg_session_minutes": 79.5}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly'),
    -- Unique Viewers - current month (REQUIRED for UsageSummary.uniqueViewers)
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'unique_viewers', 5000,
     '{"by_country": {"US": 1500, "NL": 1000, "GB": 500, "DE": 500, "JP": 500, "other": 1000}}',
     DATE_TRUNC('month', NOW()), NOW(), 'monthly')
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    usage_details = EXCLUDED.usage_details,
    granularity = EXCLUDED.granularity;

-- Previous month usage (finalized)
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end)
VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'stream_hours', 342.0,
     '{"stream_count": 8, "avg_duration_hours": 42.75}',
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'egress_gb', 1245.6,
     '{"viewer_sessions": 35000, "avg_quality": "1080p"}',
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'average_storage_gb', 72.5,
     '{"dvr_gb": 38.1, "clips_gb": 10.2, "recordings_gb": 24.2}',
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'total_streams', 8,
     '{"live_streams": 5, "vod_streams": 3}',
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'peak_viewers', 145,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-09-20T18:45:00Z"}',
     DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
     DATE_TRUNC('month', NOW()) - INTERVAL '1 second')
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    usage_details = EXCLUDED.usage_details;

-- Two months ago usage
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end)
VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'stream_hours', 215.25,
     '{"stream_count": 5, "avg_duration_hours": 43.05}',
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'egress_gb', 890.2,
     '{"viewer_sessions": 24000, "avg_quality": "720p"}',
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'average_storage_gb', 58.9,
     '{"dvr_gb": 30.5, "clips_gb": 8.4, "recordings_gb": 20.0}',
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'total_streams', 5,
     '{"live_streams": 3, "vod_streams": 2}',
     DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
     DATE_TRUNC('month', NOW() - INTERVAL '1 month') - INTERVAL '1 second'),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'peak_viewers', 112,
     '{"stream_id": "demo_live_stream_001", "timestamp": "2023-08-10T12:15:00Z"}',
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
    period_start, period_end, due_date, paid_at,
    base_amount, metered_amount, usage_details,
    created_at
) VALUES
-- Current month (draft invoice preview)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'draft',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW() + INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month' + INTERVAL '14 days',
    NULL,
    249.00,  -- Developer tier base
    0.00,    -- No metered charges yet
    '{"viewer_hours": 4166.67, "average_storage_gb": 23.5, "stream_hours": 127.5, "egress_gb": 456.78, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW())
),
-- Previous month (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW()) + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    249.00,
    0.00,
    '{"viewer_hours": 7500.0, "average_storage_gb": 45.2, "stream_hours": 342.0, "egress_gb": 1245.6, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '1 month')
),
-- Two months ago (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    249.00,
    DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    249.00,
    0.00,
    '{"viewer_hours": 5833.33, "average_storage_gb": 32.1, "stream_hours": 215.25, "egress_gb": 890.2, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '2 months')
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    paid_at = EXCLUDED.paid_at;

-- ============================================================================
-- PURSER: Demo Billing Payments (Payment Transactions)
-- ============================================================================
-- Payment records linked to paid invoices

INSERT INTO purser.billing_payments (
    id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at
) VALUES
-- Payment for previous month invoice
(
    '5eedba1d-fee5-da7a-ba1d-fee5da7a0001',
    '5eedb111-fee5-da7a-b111-fee5da7a0002',  -- Previous month paid invoice
    'directdebit',
    249.00,
    'EUR',
    'tr_demo_sepa_001',
    'confirmed',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days'
),
-- Payment for two months ago invoice
(
    '5eedba1d-fee5-da7a-ba1d-fee5da7a0002',
    '5eedb111-fee5-da7a-b111-fee5da7a0003',  -- Two months ago paid invoice
    'directdebit',
    249.00,
    'EUR',
    'tr_demo_sepa_002',
    'confirmed',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days'
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    confirmed_at = EXCLUDED.confirmed_at;

-- ============================================================================
-- PURSER: Demo Balance Transactions (Prepaid Audit Trail)
-- ============================================================================
-- Transaction history explaining the $50 prepaid balance

INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, created_at
) VALUES
(
    '5eedba1a-5ce5-da7a-ba1a-5ce5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    5000,       -- +$50.00 topup
    5000,       -- Balance after: $50.00
    'topup',
    'Initial demo balance - card payment',
    '5eedba1a-5ce5-da7a-ba1a-5ce5da7a0002',  -- Reference to a notional card payment
    'card_payment',
    NOW() - INTERVAL '7 days'
)
ON CONFLICT (tenant_id, reference_type, reference_id)
WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO UPDATE SET
    balance_after_cents = EXCLUDED.balance_after_cents;

-- ============================================================================
-- PURSER: Demo Tenant Balance Rollups (Statistics)
-- ============================================================================
-- Pre-aggregated lifetime stats matching balance_transactions

INSERT INTO purser.tenant_balance_rollups (
    tenant_id, total_topup_cents, total_topup_eur_cents, total_usage_cents,
    topup_count, first_topup_at, last_topup_at, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    5000,       -- $50.00 total topups
    4600,       -- ~€46.00 EUR equivalent
    0,          -- No usage deductions yet
    1,          -- 1 topup
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW()
)
ON CONFLICT (tenant_id) DO UPDATE SET
    total_topup_cents = EXCLUDED.total_topup_cents,
    total_usage_cents = EXCLUDED.total_usage_cents,
    topup_count = EXCLUDED.topup_count,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo Clips (Business Registry)
-- ============================================================================
-- Clip business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.clips (
    id, tenant_id, user_id, stream_id, clip_hash, artifact_internal_name, playback_id,
    title, description,
    start_time, duration, clip_mode,
    retention_until, created_at, updated_at
) VALUES
-- Demo clip (ready) - matches foghorn.artifacts entry
(
    '5eedb17e-da7a-b17e-da7a-b17eda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    '20240101120000a1b2c3d4e5f67890',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'clip_int_001',
    'clp1a2b3c4d5e6fg',
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
    '5eedb17e-da7a-b17e-da7a-b17eda7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    '20240101120100b2c3d4e5f6789012',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'clip_int_002',
    'clp2a2b3c4d5e6fh',
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
    id, tenant_id, user_id, stream_id, dvr_hash, artifact_internal_name, playback_id,
    internal_name,
    retention_until, created_at, updated_at
) VALUES
-- Demo DVR recording (completed) - matches foghorn.artifacts entry
(
    '5eedf11e-5afe-da7a-f11e-5afeda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    '20240101120200fedcba9876543210',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'dvr_int_001',
    'dvr1a2b3c4d5e6fg',
    'demo_live_stream_001',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted) - matches foghorn.artifacts entry
(
    '5eedf11e-5afe-da7a-f11e-5afeda7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    '20240101120300fedcba9876543211',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'dvr_int_002',
    'dvr2a2b3c4d5e6fh',
    'demo_live_stream_001',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (dvr_hash) DO UPDATE SET
    internal_name = EXCLUDED.internal_name,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo VOD Assets (Business Registry)
-- ============================================================================
-- VOD business metadata owned by control plane
-- These correspond to foghorn.artifacts + foghorn.vod_metadata entries for lifecycle state

INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, vod_hash, artifact_internal_name, playback_id,
    title, description, filename, content_type,
    size_bytes, retention_until, created_at, updated_at
) VALUES
-- Demo VOD (ready) - WebM sample
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '20240101120400c3d4e5f678901234',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'vod_int_001',
    'vod1a2b3c4d5e6fg',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'product_demo_2024.webm',
    'video/webm',
    149099,
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD (processing) - Still being validated
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '20240101120500d4e5f6789012345a',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'vod_int_002',
    'vod2a2b3c4d5e6fh',
    'Live Streaming Webinar',
    'Educational webinar about low-latency streaming',
    'webinar_recording.mp4',
    'video/mp4',
    104857600,
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD (failed) - Invalid format
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '20240101120600e5f6789012345678',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'vod_int_003',
    'vod3a2b3c4d5e6fi',
    'Failed Upload',
    'This file failed validation due to unsupported format',
    'corrupted_file.avi',
    'video/x-msvideo',
    15728640,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (vod_hash) DO UPDATE SET
    title = EXCLUDED.title,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: Demo Artifacts (Unified Clip/DVR Lifecycle Table)
-- ============================================================================
-- Demo artifacts for development and testing
-- Note: Business metadata (tenant_id, title, description) is in Commodore
-- Foghorn only stores lifecycle state here
-- The artifact_hash values MUST match the clip_hash/dvr_hash in commodore.clips/dvr_recordings above

INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, internal_name, artifact_internal_name, tenant_id,
    status, size_bytes, manifest_path, format,
    storage_location, sync_status, retention_until,
    created_at, updated_at
) VALUES
-- Demo clip (ready)
(
    '20240101120000a1b2c3d4e5f67890',        -- 30-char: timestamp(14) + hex(16)
    'clip',
    'demo_live_stream_001',
    'clip_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant (denormalized for fallback)
    'ready',
    140795,         -- Actual file size: ~137KB
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/20240101120000a1b2c3d4e5f67890.mp4',
    'mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '2 hours',
    NOW() - INTERVAL '2 hours'
),
-- Demo clip (deleted, for testing cleanup flows)
(
    '20240101120100b2c3d4e5f6789012',        -- 30-char: timestamp(14) + hex(16)
    'clip',
    'demo_live_stream_001',
    'clip_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'deleted',
    140795,
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/20240101120100b2c3d4e5f6789012.mp4',
    'mp4',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR recording (completed)
(
    '20240101120200fedcba9876543210',        -- 30-char: timestamp(14) + hex(16)
    'dvr',
    'demo_live_stream_001',
    'dvr_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'completed',
    513176,         -- Actual total size: ~501KB (2 segments + manifest)
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/20240101120200fedcba9876543210/20240101120200fedcba9876543210.m3u8',
    'm3u8',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted, for testing cleanup flows)
(
    '20240101120300fedcba9876543211',        -- 30-char: timestamp(14) + hex(16)
    'dvr',
    'demo_live_stream_001',
    'dvr_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'deleted',
    1024000,
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/20240101120300fedcba9876543211/20240101120300fedcba9876543211.m3u8',
    'm3u8',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD asset (ready, warmed to edge)
(
    '20240101120400c3d4e5f678901234',        -- 30-char: timestamp(14) + hex(16)
    'vod',
    NULL,                                     -- No stream association
    'vod_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'ready',
    149099,         -- Actual file size: ~146KB
    NULL,            -- No manifest for VOD (direct file playback)
    'webm',
    's3',            -- Stored in S3
    'synced',
    NOW() + INTERVAL '30 days',   -- 30-day retention for VOD
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD asset (processing, just uploaded)
(
    '20240101120500d4e5f6789012345a',        -- 30-char: timestamp(14) + hex(16)
    'vod',
    NULL,
    'vod_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'processing',
    104857600,       -- 100MB
    NULL,
    'mp4',
    's3',
    'synced',
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD asset (failed validation)
(
    '20240101120600e5f6789012345678',        -- 30-char: timestamp(14) + hex(16)
    'vod',
    NULL,
    'vod_int_003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'failed',
    15728640,        -- 15MB
    NULL,
    'avi',
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
    '20240101120400c3d4e5f678901234',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'product_demo_2024.webm',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'video/webm',
    NULL,            -- Upload completed
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/20240101120400c3d4e5f678901234/20240101120400c3d4e5f678901234.webm',
    4000,            -- 4 seconds
    '640x360',
    'vp9',
    'opus',
    300,             -- ~300 kbps
    640, 360, 30.0, 2, 48000,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD (processing) - Still being validated
(
    '20240101120500d4e5f6789012345a',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'webinar_recording.mp4',
    'Live Streaming Webinar',
    'Educational webinar about low-latency streaming',
    'video/mp4',
    'abc123multipartupload',   -- Still has upload ID (not yet cleaned)
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/20240101120500d4e5f6789012345a/20240101120500d4e5f6789012345a.mp4',
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
    '20240101120600e5f6789012345678',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'corrupted_file.avi',
    'Failed Upload',
    'This file failed validation due to unsupported format',
    'video/x-msvideo',
    NULL,
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/20240101120600e5f6789012345678/20240101120600e5f6789012345678.avi',
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
    artifact_hash, node_id, file_path, size_bytes, access_count, last_accessed, last_seen_at, is_orphaned
) VALUES
-- Demo clip on edge-node-1
(
    '20240101120000a1b2c3d4e5f67890',
    'edge-node-1',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/20240101120000a1b2c3d4e5f67890.mp4',
    140795,
    42,
    NOW() - INTERVAL '3 hours',
    NOW(),
    false
),
-- Demo DVR on edge-node-1
(
    '20240101120200fedcba9876543210',
    'edge-node-1',
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/20240101120200fedcba9876543210/20240101120200fedcba9876543210.m3u8',
    513176,
    7,
    NOW() - INTERVAL '1 day',
    NOW(),
    false
),
-- Demo VOD on edge-node-1 (warmed from S3)
(
    '20240101120400c3d4e5f678901234',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/20240101120400c3d4e5f678901234.webm',
    149099,
    128,
    NOW() - INTERVAL '2 hours',
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
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    NOW() - INTERVAL '1 hour',  -- Last processed 1 hour ago
    NOW()
) ON CONFLICT (tenant_id) DO UPDATE SET
    last_processed_at = NOW() - INTERVAL '1 hour',
    updated_at = NOW();

-- ============================================================================
-- PURSER: Usage Rollups (create daily/monthly aggregates from hourly data)
-- ============================================================================
-- This mimics what Purser's rollupUsageRecords job does in production
-- Aggregates hourly → daily → monthly

-- Rollup hourly → daily (matches Purser's rollupUsageRecords logic)
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity)
SELECT
    tenant_id,
    cluster_id,
    usage_type,
    CASE
        WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers', 'total_streams', 'total_viewers',
                            'unique_users', 'unique_users_period', 'peak_viewers',
                            'livepeer_unique_streams', 'native_av_unique_streams') THEN MAX(usage_value)
        WHEN usage_type IN ('average_storage_gb') THEN AVG(usage_value)
        ELSE SUM(usage_value)
    END,
    '{}',
    DATE_TRUNC('day', period_start),
    DATE_TRUNC('day', period_start) + INTERVAL '1 day',
    'daily'
FROM purser.usage_records
WHERE granularity = 'hourly'
GROUP BY tenant_id, cluster_id, usage_type, DATE_TRUNC('day', period_start)
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    granularity = EXCLUDED.granularity;

-- Rollup daily → monthly (matches Purser's rollupUsageRecords logic)
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity)
SELECT
    tenant_id,
    cluster_id,
    usage_type,
    CASE
        WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers', 'total_streams', 'total_viewers',
                            'unique_users', 'unique_users_period', 'peak_viewers',
                            'livepeer_unique_streams', 'native_av_unique_streams') THEN MAX(usage_value)
        WHEN usage_type IN ('average_storage_gb') THEN AVG(usage_value)
        ELSE SUM(usage_value)
    END,
    '{}',
    DATE_TRUNC('month', period_start),
    DATE_TRUNC('month', period_start) + INTERVAL '1 month',
    'monthly'
FROM purser.usage_records
WHERE granularity = 'daily'
GROUP BY tenant_id, cluster_id, usage_type, DATE_TRUNC('month', period_start)
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    granularity = EXCLUDED.granularity;
