-- Deterministic local demo fixture owned by quartermaster.
-- Applied explicitly by `make seed-demo`; never loaded by database first boot.

-- Demo seed for Quartermaster and Purser

-- Platform cluster (control + data plane: gateway, commodore, purser, skipper, quartermaster, decklog, signalman, periscope)
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    cluster_class,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'central-primary', 'Central Platform', 'central', 'platform.demo.frameworks.network',
    'platform_official',
    0, 0, 0,
    FALSE, TRUE, TRUE,
    'public', 'Platform services: API, billing, analytics, events'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    cluster_class = EXCLUDED.cluster_class,
    is_platform_official = TRUE,
    public_topology = TRUE,
    visibility = 'public',
    short_description = COALESCE(EXCLUDED.short_description, quartermaster.infrastructure_clusters.short_description);

-- Media cluster (edge nodes enroll here, served by foghorn via cluster_assignments)
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    cluster_class,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'demo-media', 'Demo Media Cluster', 'edge', 'demo.frameworks.network',
    'platform_official',
    0, 0, 0,
    TRUE, TRUE, TRUE,
    'public', 'Media cluster: edge nodes, stream routing, viewer delivery'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    cluster_class = EXCLUDED.cluster_class,
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
    owner_tenant_id, cluster_class,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description
)
VALUES (
    'demo-selfhosted', 'Demo Self-hosted Cluster', 'edge', 'selfhosted.demo.frameworks.network',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'tenant_private',
    0, 0, 0,
    FALSE, FALSE, FALSE,
    'private', 'Tenant-owned media cluster for local offload testing'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    owner_tenant_id = EXCLUDED.owner_tenant_id,
    cluster_class = EXCLUDED.cluster_class,
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

-- Grant access to demo clusters for the demo tenant
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, access_source, subscription_status, is_active
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'central-primary', 'owner', 'platform_tier', 'active', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'owner', 'platform_tier', 'active', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-selfhosted', 'owner', 'owner', 'active', TRUE)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    access_source = EXCLUDED.access_source,
    subscription_status = EXCLUDED.subscription_status,
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
