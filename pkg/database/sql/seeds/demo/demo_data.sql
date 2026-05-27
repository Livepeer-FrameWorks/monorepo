-- Demo seed for Quartermaster and Purser

-- Platform cluster (control + data plane: gateway, commodore, purser, skipper, quartermaster, decklog, signalman, periscope)
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'central-primary', 'Central Platform', 'central', 'platform.demo.frameworks.network',
    0, 0, 0,
    FALSE, TRUE, TRUE,
    'public', 'Platform services: API, billing, analytics, events'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    is_platform_official = TRUE,
    public_topology = TRUE,
    visibility = 'public',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

-- Media cluster (edge nodes enroll here, served by foghorn via cluster_assignments)
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'demo-media', 'Demo Media Cluster', 'edge', 'demo.frameworks.network',
    0, 0, 0,
    TRUE, TRUE, TRUE,
    'public', 'Media cluster: edge nodes, stream routing, viewer delivery'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    is_default_cluster = TRUE,
    is_platform_official = TRUE,
    public_topology = TRUE,
    visibility = 'public',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

-- Demo tenant (must exist before any cluster references it via owner_tenant_id FK)
INSERT INTO quartermaster.tenants (id, name, subdomain, deployment_tier, primary_cluster_id, official_cluster_id)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'Demo Organization', 'demo', 'pro', 'demo-media', 'demo-media')
ON CONFLICT (id) DO UPDATE SET
    primary_cluster_id = EXCLUDED.primary_cluster_id,
    official_cluster_id = EXCLUDED.official_cluster_id;

-- Tenant-private self-hosted cluster. This is intentionally non-platform:
-- Purser grants access through Quartermaster's general access path after
-- classifying it as tenant_private, and usage rates at zero when priced as
-- free_unmetered.
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    owner_tenant_id,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'demo-selfhosted', 'Demo Self-hosted Cluster', 'edge', 'selfhosted.demo.frameworks.network',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    0, 0, 0,
    FALSE, FALSE, FALSE,
    'private', 'Tenant-owned media cluster for local offload testing'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    owner_tenant_id = EXCLUDED.owner_tenant_id,
    is_platform_official = FALSE,
    public_topology = FALSE,
    visibility = 'private',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

-- Ensure service catalog minimal entry
INSERT INTO quartermaster.services (service_id, name, plane, description, default_port, health_check_path, docker_image, type, protocol)
VALUES ('api_tenants', 'Quartermaster', 'control', 'Tenant and cluster management service', 9008, '/health', 'frameworks/quartermaster', 'api_tenants', 'http')
ON CONFLICT (service_id) DO NOTHING;

-- Assign quartermaster to the platform cluster
INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas, config_blob)
VALUES ('central-primary', 'api_tenants', 'running', 1, '{"database_url": "postgres://frameworks_user:frameworks_dev@postgres:5432/frameworks"}')
ON CONFLICT (cluster_id, service_id) DO NOTHING;

INSERT INTO quartermaster.tenant_cluster_assignments (tenant_id, cluster_id, deployment_tier, is_primary)
VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'pro', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'pro', FALSE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-selfhosted', 'pro', FALSE)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

-- Demo user
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions)
VALUES ('5eedface-5e1f-da7a-face-5e1fda7a0001', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo@frameworks.network', '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla', 'Demo', 'User', 'owner', ARRAY['streams:read','streams:write','analytics:read','users:read','users:write','settings:write'])
ON CONFLICT DO NOTHING;

UPDATE commodore.users SET verified = TRUE WHERE email = 'demo@frameworks.network' AND tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001';

-- Service account
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified)
VALUES ('5eeddeaf-dead-beef-deaf-deadbeef0000', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'service@internal', 'no-login', 'Service', 'Account', 'service', ARRAY['*'], TRUE, TRUE)
ON CONFLICT DO NOTHING;

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

-- Local managed-stream fixture: loops the seeded VOD file through Mist's
-- always_on concrete-source path so docker-compose can exercise the same
-- Commodore -> Foghorn -> Helmsman -> Mist materialization used in production.
INSERT INTO commodore.streams (
    id, tenant_id, user_id, stream_key, playback_id, internal_name,
    title, description, ingest_mode, always_on, is_recording_enabled
)
VALUES (
    '5eedfeed-11fe-ca57-feed-11feca5700f1',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    'sk_dev_managed_vod_loop',
    'pb_dev_managed_vod_loop',
    'dev_managed_vod_loop',
    'Dev Managed VOD Loop',
    'Docker dev fixture for Mist-native always_on stream materialization',
    'mist_native',
    TRUE,
    FALSE
)
ON CONFLICT (internal_name) DO UPDATE SET
    playback_id = EXCLUDED.playback_id,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    ingest_mode = EXCLUDED.ingest_mode,
    always_on = EXCLUDED.always_on,
    is_recording_enabled = EXCLUDED.is_recording_enabled,
    updated_at = NOW();

INSERT INTO commodore.stream_mist_sources (
    stream_id, source_spec, source_kind, placement_count, allowed_cluster_ids,
    local_asset_paths
)
VALUES (
    '5eedfeed-11fe-ca57-feed-11feca5700f1',
    'ts-exec:ffmpeg -hide_banner -loglevel warning -re -stream_loop -1 -i /var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4 -c copy -f mpegts -',
    'exec',
    1,
    ARRAY['demo-media']::text[],
    '[{"path":"/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4","sha256":"1bee3aca5b2ebb305976fd678812969891bc819da9468e15d5cd00bc3b2a8131","note":"Visible seeded VOD asset mounted from infrastructure/demo-recordings."}]'::jsonb
)
ON CONFLICT (stream_id) DO UPDATE SET
    source_spec = EXCLUDED.source_spec,
    source_kind = EXCLUDED.source_kind,
    placement_count = EXCLUDED.placement_count,
    allowed_cluster_ids = EXCLUDED.allowed_cluster_ids,
    local_asset_paths = EXCLUDED.local_asset_paths,
    updated_at = NOW();

INSERT INTO commodore.stream_processing_config (stream_id, processes_live, updated_at)
VALUES (
    '5eedfeed-11fe-ca57-feed-11feca5700f1',
    '[{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]'::jsonb,
    NOW()
)
ON CONFLICT (stream_id) DO UPDATE SET
    processes_live = EXCLUDED.processes_live,
    updated_at = NOW();

-- Ensure cluster is owned by demo tenant to allow fingerprint-based association
UPDATE quartermaster.infrastructure_clusters
SET owner_tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001'
WHERE cluster_id = 'central-primary';

UPDATE quartermaster.infrastructure_clusters
SET owner_tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001'
WHERE cluster_id = 'demo-media';

-- Pre-provision a demo edge node that matches HELMSMAN_NODE_ID in docker-compose
-- Belongs to the media cluster; region matches MistServer config location
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES (
    'edge-node-1', 'demo-media', 'edge-node-1', 'edge', 'active',
    'Leiden', '127.0.0.1', '127.0.0.1', 52.1601, 4.4970, '{}', '{}'
) ON CONFLICT (node_id) DO UPDATE SET
    region = EXCLUDED.region,
    external_ip = EXCLUDED.external_ip,
    internal_ip = EXCLUDED.internal_ip,
    latitude = EXCLUDED.latitude,
    longitude = EXCLUDED.longitude;

-- Platform node for Docker dev (all control + data plane services)
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES (
    'central-node-1', 'central-primary', 'central-node-1', 'core', 'active',
    'Amsterdam', '127.0.0.1', '127.0.0.1', 52.3676, 4.9041, '{}', '{}'
) ON CONFLICT (node_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id,
    region = EXCLUDED.region,
    external_ip = EXCLUDED.external_ip,
    internal_ip = EXCLUDED.internal_ip,
    latitude = EXCLUDED.latitude,
    longitude = EXCLUDED.longitude;

-- Regional edge nodes (offline) for routing map visuals and historical data
-- These nodes are not running in docker-compose but provide geographic diversity
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES
    ('edge-ashburn', 'demo-media', 'edge-ashburn', 'edge', 'offline',
     'Ashburn', NULL, NULL, 39.0438, -77.4874, '{"region":"us-east"}', '{}'),
    ('edge-singapore', 'demo-media', 'edge-singapore', 'edge', 'offline',
     'Singapore', NULL, NULL, 1.3521, 103.8198, '{"region":"apac"}', '{}')
ON CONFLICT (node_id) DO NOTHING;

-- Billing tiers required by the demo subscription rows below. Production
-- clusters reconcile the canonical catalog through purser bootstrap; this file
-- is only the Docker dev/demo fixture loaded on a fresh volume.
--
-- Pricing rules (purser.tier_pricing_rules) and entitlements
-- (purser.tier_entitlements) are seeded below as separate rows.
WITH demo_process_config AS (
    SELECT
        '[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none","x-LSP-name":"Audio to Opus"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none","x-LSP-name":"Audio to AAC"},{"process":"Thumbs","x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_live,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_dvr,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_clip,
        '[{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true,"x-LSP-name":"Thumbnail Sprites"}]'::jsonb AS processes_dvr_finalize,
        '[{"process":"AV","codec":"opus","track_inhibit":"audio=opus","track_select":"video=none"},{"process":"AV","codec":"AAC","track_inhibit":"audio=aac","track_select":"video=none"},{"process":"Thumbs","track_select":"video=maxbps","track_inhibit":"subtitle=all","inconsequential":true,"exit_unmask":true}]'::jsonb AS processes_vod
)
INSERT INTO purser.billing_tiers (
    tier_name, display_name, description, base_price, currency,
    features, support_level, sla_level, metering_enabled,
    tier_level, is_enterprise, is_default_prepaid, is_default_postpaid,
    processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
)
SELECT
    v.tier_name, v.display_name, v.description, v.base_price, v.currency,
    v.features::jsonb, v.support_level, v.sla_level, v.metering_enabled,
    v.tier_level, v.is_enterprise, v.is_default_prepaid, v.is_default_postpaid,
    pc.processes_live, pc.processes_dvr, pc.processes_clip, pc.processes_dvr_finalize, pc.processes_vod
FROM (VALUES
('payg', 'Pay As You Go', 'Prepaid pay-as-you-go pricing with no included usage.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', true, 0, false, true, false),
('free', 'Free', 'Self-hosted with Livepeer transcoding. Watermarked player, no SLA.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', true, 1, false, false, true),
('supporter', 'Supporter', '120K delivered mins, hosted LB, custom subdomain. ~100-300 viewers.', 79.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "basic"}',
'basic', 'none', true, 2, false, false, false),
('developer', 'Developer', '500K delivered mins, priority processing, team features, advanced analytics. ~500-1K viewers.', 249.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "priority"}',
'priority', 'standard', true, 3, false, false, false),
('production', 'Production', '2M delivered mins, dedicated processing capacity, 24/7 support + SLA. ~2-5K viewers.', 999.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "enterprise"}',
'enterprise', 'premium', true, 4, false, false, false),
('enterprise', 'Enterprise', 'Custom capacity, private deployments, dedicated support, custom SLAs. Contact us.', 0.00, 'EUR',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "dedicated", "processing_customizable": true}',
'dedicated', 'custom', true, 5, true, false, false)
) AS v(
    tier_name, display_name, description, base_price, currency,
    features, support_level, sla_level, metering_enabled,
    tier_level, is_enterprise, is_default_prepaid, is_default_postpaid
)
CROSS JOIN demo_process_config pc
ON CONFLICT (tier_name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    base_price = EXCLUDED.base_price,
    features = EXCLUDED.features,
    support_level = EXCLUDED.support_level,
    sla_level = EXCLUDED.sla_level,
    metering_enabled = EXCLUDED.metering_enabled,
    tier_level = EXCLUDED.tier_level,
    is_enterprise = EXCLUDED.is_enterprise,
    is_default_prepaid = EXCLUDED.is_default_prepaid,
    is_default_postpaid = EXCLUDED.is_default_postpaid,
    processes_live = EXCLUDED.processes_live,
    processes_dvr = EXCLUDED.processes_dvr,
    processes_clip = EXCLUDED.processes_clip,
    processes_dvr_finalize = EXCLUDED.processes_dvr_finalize,
    processes_vod = EXCLUDED.processes_vod;

-- Tier cap on customer-set retention. 0 = no cap (paid baseline);
-- Free's finite cap is the anti-abuse guardrail. Per-class system
-- defaults (VOD: keep forever, DVR/clip: 30d) live in Commodore code,
-- not in entitlements.
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, 'recording_retention_days', to_jsonb(v.days)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 30), ('supporter', 0), ('developer', 0), ('production', 0)
) AS v(tier_name, days) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- storage_limit_gb is the hard runtime cap on current durable artifact bytes
-- (point-in-time, distinct from the time-weighted storage_gb_seconds_cold
-- billing meter). Foghorn rejects new durable writes when a tenant is at cap.
-- Free tier only; paid tiers have no point-in-time cap (storage is metered).
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, 'storage_limit_gb', to_jsonb(v.gb)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 10)
) AS v(tier_name, gb) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- Free-plan concurrent fair-use caps. These are tenant-plan policy, not static
-- media-cluster capacity; cluster capacity is reported dynamically by edges.
INSERT INTO purser.tier_entitlements (tier_id, key, value)
SELECT bt.id, v.key, to_jsonb(v.value)
FROM purser.billing_tiers bt
JOIN (VALUES
    ('free', 'max_concurrent_streams', 3),
    ('free', 'max_concurrent_viewers', 200)
) AS v(tier_name, key, value) ON v.tier_name = bt.tier_name
ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value;

-- Tier pricing rules (one row per tier x meter).
INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price, config)
SELECT bt.id, r.meter, r.model, 'EUR', r.included_quantity, r.unit_price, '{}'::jsonb
FROM purser.billing_tiers bt
JOIN (VALUES
    ('payg', 'delivered_minutes', 'tiered_graduated', 0, 0.00055),
    ('payg', 'storage_gb_seconds_cold', 'all_usage', 0, 0.035),
    ('free', 'delivered_minutes', 'tiered_graduated', 10000, 0),
    ('free', 'storage_gb_seconds_cold', 'tiered_graduated', 7200, 0),
    ('supporter', 'delivered_minutes', 'tiered_graduated', 120000, 0.00055),
    ('supporter', 'storage_gb_seconds_cold', 'all_usage', 0, 0.035),
    ('developer', 'delivered_minutes', 'tiered_graduated', 500000, 0.00052),
    ('developer', 'storage_gb_seconds_cold', 'all_usage', 0, 0.030),
    ('production', 'delivered_minutes', 'tiered_graduated', 2000000, 0.00050),
    ('production', 'storage_gb_seconds_cold', 'all_usage', 0, 0.025)
) AS r(tier_name, meter, model, included_quantity, unit_price)
ON r.tier_name = bt.tier_name
ON CONFLICT (tier_id, meter) DO UPDATE SET
    model = EXCLUDED.model,
    currency = EXCLUDED.currency,
    included_quantity = EXCLUDED.included_quantity,
    unit_price = EXCLUDED.unit_price,
    config = EXCLUDED.config;

-- Demo subscription in Purser
INSERT INTO purser.tenant_subscriptions (
    tenant_id, tier_id, status, billing_email, started_at, next_billing_date,
    billing_period_start, billing_period_end
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', bt.id, 'active', 'demo@frameworks.network',
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

-- Demo prepaid balance for the demo tenant (starts at EUR 50)
INSERT INTO purser.prepaid_balances (
    tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 5000, 'EUR', 500, NOW() - INTERVAL '7 days', NOW()
) ON CONFLICT (tenant_id, currency) DO UPDATE SET
    balance_cents = EXCLUDED.balance_cents,
    updated_at = NOW();

-- Demo cluster subscription tracking (paid cluster flow uses this table)
INSERT INTO purser.cluster_subscriptions (
    tenant_id, cluster_id, status, created_at, updated_at
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'active', NOW(), NOW()),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'active', NOW(), NOW()),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-selfhosted', 'active', NOW(), NOW())
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    status = EXCLUDED.status,
    updated_at = NOW();

-- Cluster pricing for demo clusters
INSERT INTO purser.cluster_pricing (
    cluster_id, pricing_model,
    allow_free_tier, required_tier_level,
    default_quotas
) VALUES
    (
        'central-primary', 'free_unmetered',
        TRUE, 0,
        '{"retention_days": 7}'
    ),
    (
        'demo-selfhosted', 'free_unmetered',
        TRUE, 0,
        '{"retention_days": 30}'
    )
ON CONFLICT (cluster_id) DO UPDATE SET
    pricing_model = 'free_unmetered',
    allow_free_tier = TRUE;

INSERT INTO purser.platform_fee_policy (
    cluster_kind, cluster_owner_tenant_id, pricing_source, fee_basis_points, notes
)
VALUES (
    'third_party_marketplace', NULL, NULL, 2000, 'default marketplace revenue-share policy'
)
ON CONFLICT DO NOTHING;

-- Grant access to demo clusters for the demo tenant
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, is_active
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'owner', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'owner', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-selfhosted', 'owner', TRUE)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
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
    tenant_id = EXCLUDED.tenant_id,
    fingerprint_machine_sha256 = EXCLUDED.fingerprint_machine_sha256,
    fingerprint_macs_sha256 = EXCLUDED.fingerprint_macs_sha256,
    attrs = EXCLUDED.attrs,
    last_seen = NOW();

-- Demo bootstrap token for node provisioning testing
-- This token was used to provision edge-node-1
INSERT INTO quartermaster.bootstrap_tokens (
    id, token_hash, token_prefix, kind, name,
    tenant_id, cluster_id, expected_ip,
    metadata, usage_limit, usage_count,
    expires_at, used_at, created_by, created_at
) VALUES (
    '5eedb007-5eed-da7a-b007-5eedda7a0001',
    '758457699e76c8a3398ad27d0c9535949df07f07ebe5fcdf413846019123f5e6', -- sha256("demo_bootstrap_token_for_local_development_testing_only")
    'demo_bootstr...',
    'edge_node',
    'Demo Edge Node Bootstrap',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',                               -- Edge enrollment targets the media cluster
    NULL,                                     -- Allow docker bridge IPs in local dev
    '{"purpose": "demo", "environment": "development"}',
    10,    -- Max 10 uses
    1,     -- Already used once for edge-node-1
    NOW() + INTERVAL '30 days',
    NOW() - INTERVAL '1 day',                 -- Used yesterday
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Created by demo user
    NOW() - INTERVAL '2 days'
) ON CONFLICT (token_hash) DO UPDATE SET
    expires_at = NOW() + INTERVAL '30 days';

-- ============================================================================
-- PURSER: 5-Minute Usage Records (raw data, like Periscope produces)
-- ============================================================================
-- Generate 7 days of 5-minute granularity usage records
-- 7 days * 24 hours * 12 intervals/hour = 2016 records per usage type
-- These are the canonical Purser rows used by invoices and usage charts.

-- 7 days of 5-minute delta rows on canonical, 5-min-aligned boundaries.
-- value_kind='delta' is required for rated meters to feed cluster_rating /
-- the rating engine; other value shapes are excluded from invoices.
INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity, value_kind)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'central-primary',
    usage_type,
    CASE
        WHEN usage_type = 'storage_gb_seconds_cold' THEN base_value * 300.0 * (0.7 + 0.6 * random())
        WHEN usage_type IN ('max_viewers', 'total_streams', 'total_viewers') THEN base_value * (0.7 + 0.6 * random())
        ELSE base_value / 288.0 * (0.7 + 0.6 * random())
    END,
    CASE
        WHEN usage_type = 'media_seconds' THEN
            '{"codec_seconds":{"h264":8.0,"hevc":1.0,"vp9":2.0,"av1":1.0,"aac":0.5,"opus":0.25},"process_seconds":{"Livepeer":9.0,"AV":3.75}}'::jsonb
        ELSE '{}'::jsonb
    END,
    date_trunc('hour', NOW()) + ((floor(extract(epoch FROM NOW() - date_trunc('hour', NOW())) / 300) - n) * INTERVAL '5 minutes'),
    date_trunc('hour', NOW()) + ((floor(extract(epoch FROM NOW() - date_trunc('hour', NOW())) / 300) - n + 1) * INTERVAL '5 minutes'),
    'minute_5',
    'delta'
FROM generate_series(0, 2015) AS n
CROSS JOIN (VALUES
    ('stream_runtime_seconds', 64800.0),
    ('ingress_gb', 18.0),
    ('egress_gb', 65.0),
    ('storage_gb_seconds_cold', 12.0),
    ('delivered_minutes', 5100.0),
    ('media_seconds', 3672.0),
    ('max_viewers', 140.0),
    ('total_streams', 3.0),
    ('total_viewers', 420.0)
) AS usage_types(usage_type, base_value)
ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
    usage_value = EXCLUDED.usage_value,
    granularity = EXCLUDED.granularity,
    value_kind = EXCLUDED.value_kind;

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
    249.71,
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW() + INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) + INTERVAL '1 month' + INTERVAL '14 days',
    NULL,
    249.00,  -- Developer tier base
    0.71,    -- Storage: 23.5 GiB-hours x EUR 0.030
    '{"delivered_minutes": 250000.2, "storage_gb_seconds_cold": 84600.0, "stream_runtime_seconds": 459000.0, "ingress_gb": 82.0, "egress_gb": 456.78, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW())
),
-- Previous month (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    250.36,
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()),
    DATE_TRUNC('month', NOW()) + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) + INTERVAL '5 days',
    249.00,
    1.36,
    '{"delivered_minutes": 450000.0, "storage_gb_seconds_cold": 162720.0, "stream_runtime_seconds": 1231200.0, "ingress_gb": 214.0, "egress_gb": 1245.6, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '1 month')
),
-- Two months ago (paid invoice)
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'paid',
    'EUR',
    249.96,
    DATE_TRUNC('month', NOW() - INTERVAL '2 months'),
    DATE_TRUNC('month', NOW() - INTERVAL '1 month'),
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '14 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    249.00,
    0.96,
    '{"delivered_minutes": 349999.8, "storage_gb_seconds_cold": 115560.0, "stream_runtime_seconds": 774900.0, "ingress_gb": 156.0, "egress_gb": 890.2, "tier_info": {"tier_name": "developer", "display_name": "Developer", "base_price": 249.0, "metering_enabled": true}}',
    DATE_TRUNC('month', NOW() - INTERVAL '2 months')
)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status,
    amount = EXCLUDED.amount,
    base_amount = EXCLUDED.base_amount,
    metered_amount = EXCLUDED.metered_amount,
    usage_details = EXCLUDED.usage_details,
    paid_at = EXCLUDED.paid_at,
    updated_at = NOW();

-- Demo invoice line items. The runtime writer stores these transactionally with
-- every invoice; seed data does the same so a fresh dev DB exercises the
-- line-item rendering path instead of falling back to invoice aggregates.
INSERT INTO purser.invoice_line_items (
    invoice_id, tenant_id, line_key, meter, description,
    quantity, included_quantity, billable_quantity,
    unit_price, amount, currency,
    cluster_id, cluster_kind, pricing_source
) VALUES
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW()), 'YYYYMM'),
    'delivered_minutes',
    'Delivered minutes',
    250000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW()), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'Cold storage',
    23.5, 0, 23.5, 0.030, 0.71, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '1 month'), 'YYYYMM'),
    'delivered_minutes',
    'Delivered minutes',
    450000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '1 month'), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'Cold storage',
    45.2, 0, 45.2, 0.030, 1.36, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'base_subscription',
    NULL,
    'Base subscription',
    1, 0, 1, 249.00, 249.00, 'EUR',
    NULL, NULL, 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:delivered_minutes:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '2 months'), 'YYYYMM'),
    'delivered_minutes',
    'Delivered minutes',
    350000, 500000, 0, 0.00052, 0.00, 'EUR',
    'demo-media', 'platform_official', 'tier'
),
(
    '5eedb111-fee5-da7a-b111-fee5da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'meter:storage_gb_seconds_cold:demo-media:' || TO_CHAR(DATE_TRUNC('month', NOW() - INTERVAL '2 months'), 'YYYYMM'),
    'storage_gb_seconds_cold',
    'Cold storage',
    32.1, 0, 32.1, 0.030, 0.96, 'EUR',
    'demo-media', 'platform_official', 'tier'
)
ON CONFLICT (invoice_id, line_key) DO UPDATE SET
    tenant_id = EXCLUDED.tenant_id,
    meter = EXCLUDED.meter,
    description = EXCLUDED.description,
    quantity = EXCLUDED.quantity,
    included_quantity = EXCLUDED.included_quantity,
    billable_quantity = EXCLUDED.billable_quantity,
    unit_price = EXCLUDED.unit_price,
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    cluster_id = EXCLUDED.cluster_id,
    cluster_kind = EXCLUDED.cluster_kind,
    pricing_source = EXCLUDED.pricing_source,
    updated_at = NOW();

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
    'card',
    250.36,
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
    'card',
    249.96,
    'EUR',
    'tr_demo_sepa_002',
    'confirmed',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days',
    DATE_TRUNC('month', NOW()) - INTERVAL '1 month' + INTERVAL '3 days'
)
ON CONFLICT (id) DO UPDATE SET
    amount = EXCLUDED.amount,
    currency = EXCLUDED.currency,
    status = EXCLUDED.status,
    confirmed_at = EXCLUDED.confirmed_at;

-- ============================================================================
-- PURSER: Demo Balance Transactions (Prepaid Audit Trail)
-- ============================================================================
-- Transaction history explaining the EUR 50 prepaid balance

INSERT INTO purser.balance_transactions (
    id, tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, created_at
) VALUES
(
    '5eedba1a-5ce5-da7a-ba1a-5ce5da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    5000,       -- +EUR 50.00 topup
    5000,       -- Balance after: EUR 50.00
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
    5000,       -- EUR 50.00 total topups
    5000,       -- EUR 50.00 EUR equivalent
    0,          -- No usage deductions yet
    1,          -- 1 topup
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW() - INTERVAL '7 days',
    NOW()
)
ON CONFLICT (tenant_id) DO UPDATE SET
    total_topup_cents = EXCLUDED.total_topup_cents,
    total_topup_eur_cents = EXCLUDED.total_topup_eur_cents,
    total_usage_cents = EXCLUDED.total_usage_cents,
    topup_count = EXCLUDED.topup_count,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo Clips (Business Registry)
-- ============================================================================
-- Clip business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.clips (
    id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id,
    title, description,
    start_time, duration, clip_mode,
    origin_cluster_id, retention_until, created_at, updated_at
) VALUES
-- Demo clip (ready) - matches foghorn.artifacts entry
(
    '5eedb17e-da7a-b17e-da7a-b17eda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    'a1b2c3d4e5f6789012345678901234ab',      -- Must match foghorn.artifacts + on-disk filename
    'clip_int_001',
    'clp1a2b3c4d5e6fg',
    'Demo Highlight Reel',
    'Amazing gameplay highlights from the demo stream',
    1640995200000,  -- Unix timestamp (ms): Jan 1, 2022 00:00:00 UTC
    5000,           -- Duration (ms): fixture is 5 seconds
    'absolute',
    'demo-media',
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
    'demo-media',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (clip_hash) DO UPDATE SET
    title = EXCLUDED.title,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo DVR Recordings (Business Registry)
-- ============================================================================
-- DVR recording business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.dvr_recordings (
    id, tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id,
    stream_internal_name,
    origin_cluster_id, retention_until, created_at, updated_at
) VALUES
-- Demo DVR recording (completed) - matches foghorn.artifacts entry
(
    '5eedf11e-5afe-da7a-f11e-5afeda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    'fedcba98765432109876543210fedcba',      -- Must match foghorn.artifacts + on-disk filename
    'dvr_int_001',
    'dvr1a2b3c4d5e6fg',
    'demo_live_stream_001',
    'demo-media',
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
    'demo-media',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (dvr_hash) DO UPDATE SET
    internal_name = EXCLUDED.internal_name,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    updated_at = NOW();

INSERT INTO commodore.dvr_chapter_playback (
    chapter_id, tenant_id, playback_id, artifact_hash, created_at, updated_at
) VALUES (
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'chp_demo_recording_001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
)
ON CONFLICT (chapter_id) DO UPDATE SET
    playback_id = EXCLUDED.playback_id,
    artifact_hash = EXCLUDED.artifact_hash,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo VOD Assets (Business Registry)
-- ============================================================================
-- VOD business metadata owned by control plane
-- These correspond to foghorn.artifacts + foghorn.vod_metadata entries for lifecycle state

INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, vod_hash, internal_name, playback_id,
    title, description, filename, content_type,
    size_bytes, origin_cluster_id, retention_until, library_visible, created_at, updated_at
) VALUES
-- Demo VOD (ready) - HLS-compatible MP4 sample
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    'c3d4e5f678901234567890123456abcd',      -- Must match foghorn.artifacts + on-disk filename
    'vod_int_001',
    'vod1a2b3c4d5e6fg',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'product_demo_2024.mp4',
    'video/mp4',
    107553,
    'demo-media',
    NOW() + INTERVAL '30 days',
    TRUE,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Hidden VOD artifact backing the seeded DVR chapter playback ID.
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0004',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'chp_demo_recording_001',
    'Demo Stream Chapter',
    'Finalized chapter artifact for the seeded DVR recording',
    'demo_recording_chapter.mp4',
    'video/mp4',
    336471,
    'demo-media',
    NOW() + INTERVAL '7 days',
    FALSE,
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
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
    'demo-media',
    NOW() + INTERVAL '30 days',
    FALSE,
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
    'demo-media',
    NOW() - INTERVAL '1 day',
    FALSE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (vod_hash) DO UPDATE SET
    title = EXCLUDED.title,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    size_bytes = EXCLUDED.size_bytes,
    library_visible = EXCLUDED.library_visible,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: Demo Artifacts (Unified Clip/DVR Lifecycle Table)
-- ============================================================================
-- Demo artifacts for development and testing
-- Note: Business metadata (tenant_id, title, description) is in Commodore
-- Foghorn only stores lifecycle state here
-- The artifact_hash values MUST match the clip_hash/dvr_hash in commodore.clips/dvr_recordings above

INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, stream_internal_name, internal_name, tenant_id,
    status, size_bytes, manifest_path, format,
    storage_location, sync_status, retention_until, library_visible,
    created_at, updated_at
) VALUES
-- Demo clip (ready)
(
    'a1b2c3d4e5f6789012345678901234ab',      -- Must match on-disk filename
    'clip',
    'demo_live_stream_001',
    'clip_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant (denormalized for fallback)
    'ready',
    107553,         -- Browser-safe H.264/AAC fixture
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    'mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    TRUE,
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
    TRUE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR recording (completed)
(
    'fedcba98765432109876543210fedcba',      -- Must match on-disk directory/filename
    'dvr',
    'demo_live_stream_001',
    'dvr_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'completed',
    513176,         -- Actual total size: ~501KB (2 segments + manifest)
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    'm3u8',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    TRUE,
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
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/20240101120300fedcba9876543211',
    'm3u8',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    TRUE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR chapter artifact (hidden finalized VOD for the seeded recording)
(
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'vod',
    'demo_live_stream_001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'ready',
    336471,
    NULL,
    'mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',
    FALSE,
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo VOD asset (ready, warmed to edge)
(
    'c3d4e5f678901234567890123456abcd',      -- Must match on-disk filename
    'vod',
    NULL,                                     -- No stream association
    'vod_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'ready',
    107553,         -- H264/AAC fixture compatible with HLS/MP4 playback
    NULL,            -- No manifest for VOD (direct file playback)
    'mp4',
    'local',         -- On disk, pending sync to S3
    'pending',
    NOW() + INTERVAL '30 days',   -- 30-day retention for VOD
    TRUE,
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
    FALSE,
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
    FALSE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    status = EXCLUDED.status,
    size_bytes = EXCLUDED.size_bytes,
    manifest_path = EXCLUDED.manifest_path,
    format = EXCLUDED.format,
    storage_location = EXCLUDED.storage_location,
    sync_status = EXCLUDED.sync_status,
    library_visible = EXCLUDED.library_visible,
    updated_at = NOW();

UPDATE foghorn.artifacts
SET artifact_type = 'vod',
    stream_internal_name = 'demo_live_stream_001',
    internal_name = '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    status = 'ready',
    size_bytes = 336471,
    manifest_path = NULL,
    format = 'mp4',
    storage_location = 'local',
    sync_status = 'pending',
    origin_type = 'dvr_chapter',
    origin_id = '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    library_visible = FALSE,
    updated_at = NOW()
WHERE artifact_hash = '34d74b7acd7ec8cf78f6cc8c9f031a8a';

-- ============================================================================
-- FOGHORN: DVR segment ledger (per-segment source of truth)
-- ============================================================================
-- Seed rows match the on-disk media at
-- infrastructure/demo-recordings/dvr/<tenant>/<hash>/segments/segment_*.ts
-- which is bind-mounted into MistServer at /var/lib/mistserver/recordings.
-- status='pending' is honest: the sidecar startup reconciliation will upload
-- these to S3 (in dev environments with S3 creds) and flip them to 'uploaded'.
-- Until then the segments stay as recovery-source-only durability for
-- chapter finalization; they are not a playback surface.

INSERT INTO foghorn.dvr_segments (
    artifact_hash, segment_name, sequence,
    media_start_ms, media_end_ms, duration_ms,
    size_bytes, s3_key, status, created_at
) VALUES
(
    'fedcba98765432109876543210fedcba',
    'segment_0.ts', 0,
    1779105600000, 1779105610417,
    10417,
    NULL,
    'dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/segments/segment_0.ts',
    'pending', NOW() - INTERVAL '4 hours'
),
(
    'fedcba98765432109876543210fedcba',
    'segment_1.ts', 1,
    1779105610417, 1779105618000,
    7583,
    NULL,
    'dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/segments/segment_1.ts',
    'pending', NOW() - INTERVAL '4 hours'
)
ON CONFLICT (artifact_hash, segment_name) DO NOTHING;

-- ============================================================================
-- FOGHORN: DVR chapter window (virtual view over the segment ledger)
-- ============================================================================
-- One fixed-interval chapter spans the seeded recording. Its playback
-- surface is the hidden VOD artifact above, matching production chapter
-- finalization rather than the retired chapter-manifest path.

-- Demo chapter row: a single fixed-interval chapter covering the
-- recorded DVR window. chapter_id is the canonical
-- BuildChapterID(artifact_hash, mode, intervalSeconds, start_ms, end_ms)
-- so chapter-sweeper / direct lookups find this row instead of
-- regenerating a sibling:
--   sha256("fedcba98765432109876543210fedcba|fixed_interval|3600|1779105600000|1779105618000")[:32]
--   = 34d74b7acd7ec8cf78f6cc8c9f031a8a
--
INSERT INTO foghorn.dvr_chapters (
    chapter_id, artifact_hash, mode, interval_seconds,
    start_ms, end_ms, is_current,
    state, playback_artifact_hash,
    playback_id, segment_count, has_gaps,
    actual_media_start_ms, actual_media_end_ms,
    created_at
) VALUES (
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'fedcba98765432109876543210fedcba',
    'fixed_interval', 3600,
    1779105600000, 1779105618000,
    false,
    'finalized', '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'chp_demo_recording_001', 2, false,
    1779105600000, 1779105618000,
    NOW() - INTERVAL '4 hours'
)
ON CONFLICT (chapter_id) DO UPDATE SET
    state = EXCLUDED.state,
    playback_artifact_hash = EXCLUDED.playback_artifact_hash,
    playback_id = EXCLUDED.playback_id,
    segment_count = EXCLUDED.segment_count,
    has_gaps = EXCLUDED.has_gaps,
    actual_media_start_ms = EXCLUDED.actual_media_start_ms,
    actual_media_end_ms = EXCLUDED.actual_media_end_ms;

-- ============================================================================
-- FOGHORN: VOD Metadata (User-Uploaded Video Details)
-- ============================================================================
-- VOD-specific metadata like title, description, codecs, duration

INSERT INTO foghorn.vod_metadata (
    artifact_hash, filename, title, description, content_type,
    s3_upload_id, s3_key, upload_expires_at, total_parts,
    duration_ms, resolution, video_codec, audio_codec, bitrate_kbps,
    width, height, fps, audio_channels, audio_sample_rate,
    created_at, updated_at
) VALUES
-- Demo VOD (ready) - Product demo video
(
    'c3d4e5f678901234567890123456abcd',      -- Must match foghorn.artifacts + on-disk filename
    'product_demo_2024.mp4',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'video/mp4',
    NULL,            -- Upload completed
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/c3d4e5f678901234567890123456abcd/c3d4e5f678901234567890123456abcd.mp4',
    NULL,
    1,
    5000,            -- 5 seconds
    '640x360',
    'h264',
    'aac',
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
    NOW() + INTERVAL '90 minutes',
    5,
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
    NOW() - INTERVAL '1 day',
    1,
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
    'a1b2c3d4e5f6789012345678901234ab',
    'edge-node-1',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    107553,
    42,
    NOW() - INTERVAL '3 hours',
    NOW(),
    false
),
-- Demo DVR on edge-node-1
(
    'fedcba98765432109876543210fedcba',
    'edge-node-1',
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    513176,
    7,
    NOW() - INTERVAL '1 day',
    NOW(),
    false
),
-- Finalized chapter VOD for the seeded DVR on edge-node-1
(
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/34d74b7acd7ec8cf78f6cc8c9f031a8a.mp4',
    336471,
    7,
    NOW() - INTERVAL '3 hours',
    NOW(),
    false
),
-- Demo VOD on edge-node-1 (warmed from S3)
(
    'c3d4e5f678901234567890123456abcd',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    107553,
    128,
    NOW() - INTERVAL '2 hours',
    NOW(),
    false
)
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = EXCLUDED.file_path,
    size_bytes = EXCLUDED.size_bytes,
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
-- SERVICE CATALOG (seeded with exact type IDs matching self-registration)
-- ============================================================================
-- In production, ensureServiceExists creates these on first bootstrap.
-- Seed them so the map and GetClusterRouting work before first boot.

INSERT INTO quartermaster.services (service_id, name, plane, description, default_port, health_check_path, docker_image, type, protocol) VALUES
    ('bridge', 'Bridge', 'control', 'GraphQL API gateway', 18000, '/health', 'frameworks/bridge', 'bridge', 'http'),
    ('commodore', 'Commodore', 'control', 'Stream control plane', 18001, '/health', 'frameworks/commodore', 'commodore', 'http'),
    ('foghorn', 'Foghorn', 'media', 'Stream balancing and edge control service', 18008, '/health', 'frameworks/foghorn', 'foghorn', 'http'),
    ('periscope-query', 'Periscope', 'data', 'Analytics query service', 18004, '/health', 'frameworks/periscope', 'periscope-query', 'http'),
    ('purser', 'Purser', 'control', 'Billing and metering service', 18003, '/health', 'frameworks/purser', 'purser', 'http'),
    ('skipper', 'Skipper', 'control', 'AI assistant service', 18018, '/health', 'frameworks/skipper', 'skipper', 'http'),
    ('signalman', 'Signalman', 'data', 'Real-time signaling service', 18009, '/health', 'frameworks/signalman', 'signalman', 'http'),
    ('decklog', 'Decklog', 'data', 'Service event firehose', 18006, '/health', 'frameworks/decklog', 'decklog', 'grpc'),
    ('periscope-ingest', 'Periscope Ingest', 'data', 'Analytics ingest service', 18005, '/health', 'frameworks/periscope-ingest', 'periscope-ingest', 'http'),
    ('livepeer-gateway', 'Livepeer Gateway', 'media', 'Livepeer network transcoding gateway', 8935, NULL, NULL, 'livepeer-gateway', 'https'),
    ('livepeer-signer', 'Livepeer Signer', 'control', 'Livepeer remote transaction signer', 18016, NULL, NULL, 'livepeer-signer', 'http')
ON CONFLICT (service_id) DO NOTHING;

-- Assign control + data plane services to the platform cluster
INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas) VALUES
    ('central-primary', 'bridge', 'running', 1),
    ('central-primary', 'commodore', 'running', 1),
    ('central-primary', 'purser', 'running', 1),
    ('central-primary', 'skipper', 'running', 1),
    ('central-primary', 'signalman', 'running', 1),
    ('central-primary', 'decklog', 'running', 1),
    ('central-primary', 'periscope-query', 'running', 1),
    ('central-primary', 'periscope-ingest', 'running', 1)
ON CONFLICT (cluster_id, service_id) DO NOTHING;

-- Foghorn runs on the platform cluster and publishes under assigned logical clusters.
INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas) VALUES
    ('central-primary', 'foghorn', 'running', 2)
ON CONFLICT (cluster_id, service_id) DO NOTHING;

-- Livepeer Gateway + Signer are prod-only services (not in docker-compose).
-- Cluster services and service instances are provisioned by the CLI at deploy time.

-- ============================================================================
-- FOGHORN: Pre-seeded service instances for HA pair (gRPC only)
-- ============================================================================
-- Each foghorn registers a single gRPC service instance. At runtime,
-- BootstrapService matches by stable FOGHORN_INSTANCE_ID first, then by
-- (service_id, cluster_id, protocol, port, node_id, advertise_host)
-- and UPDATEs the pre-seeded row (preserving instance_id and UUID).
-- LoadServedClusters queries by FOGHORN_INSTANCE_ID → finds cluster assignments.

INSERT INTO quartermaster.service_instances (
    id, instance_id, cluster_id, node_id, service_id,
    protocol, advertise_host, port, status, health_status,
    started_at, created_at, updated_at
) VALUES
-- foghorn-1 gRPC (control plane + relay, docker-compose default 18019)
(
    '5eedf0e1-0001-da7a-f0e1-0001da7a0001',
    'foghorn-1', 'central-primary', 'central-node-1', 'foghorn',
    'grpc', 'foghorn', 18019, 'running', 'unknown',
    NOW(), NOW(), NOW()
),
-- foghorn-2 gRPC (internal control plane + relay, docker-compose default 18019)
(
    '5eedf0e1-0002-da7a-f0e1-0002da7a0002',
    'foghorn-2', 'central-primary', 'central-node-1', 'foghorn',
    'grpc', 'foghorn-2', 18019, 'running', 'unknown',
    NOW(), NOW(), NOW()
)
ON CONFLICT (instance_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id,
    node_id = EXCLUDED.node_id,
    service_id = EXCLUDED.service_id,
    protocol = EXCLUDED.protocol,
    advertise_host = EXCLUDED.advertise_host,
    port = EXCLUDED.port,
    status = 'running',
    health_status = 'unknown',
    stopped_at = NULL,
    updated_at = NOW();

-- Assign HA Foghorn instances to the platform cluster and the demo media cluster.
-- Foghorn HA is infrastructure-level: one HA pair may serve multiple media
-- clusters, while edge/node state is shared through the Redis state store.
INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id) VALUES
    ('5eedf0e1-0001-da7a-f0e1-0001da7a0001', 'central-primary'),
    ('5eedf0e1-0001-da7a-f0e1-0001da7a0001', 'demo-media'),
    ('5eedf0e1-0002-da7a-f0e1-0002da7a0002', 'central-primary'),
    ('5eedf0e1-0002-da7a-f0e1-0002da7a0002', 'demo-media')
ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW();
