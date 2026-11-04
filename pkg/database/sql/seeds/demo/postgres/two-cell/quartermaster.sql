-- Second media cell (docker compose profile two-cell), applied after the ordinary demo
-- seed by scripts/stack/up.sh and scripts/verify-two-cell-media.sh; never on first boot.
-- It mirrors cell A: foghorn-b identifies as the platform-official us-primary cluster
-- (its own cell, US region) and serves the platform media cluster demo-media-us, where
-- edge-b enrolls, the way the central Foghorn pair serves demo-media. Every customer the
-- platform tier admits can ingest in either cell. The demo tenant's private cluster
-- demo-selfhosted moves under this cell too, so foghorn-b also serves it; its edge
-- (edge-private, profile selfhosted) runs only in the private-cluster proof. Each cell
-- may serve the other's content.

INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    cluster_class,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description,
    cell_id, control_cell_id, region_id, eligible_serving_cell_ids, owner_tenant_id
)
VALUES (
    'us-primary', 'US Platform', 'central', 'us.platform.demo.frameworks.network',
    'platform_official',
    0, 0, 0,
    FALSE, TRUE, TRUE,
    'public', 'Second platform cell for the two-cell media proof',
    'us-primary', 'us-primary', 'us-east', ARRAY['us-primary', 'central-primary'],
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    is_platform_official = TRUE, is_active = TRUE,
    cell_id = 'us-primary', control_cell_id = 'us-primary', region_id = 'us-east',
    eligible_serving_cell_ids = EXCLUDED.eligible_serving_cell_ids, updated_at = NOW();

-- Cell B's platform media cluster, the counterpart of demo-media (staging-media-us on
-- staging). Its tier_inherit pricing row is in the demo Purser seed.
INSERT INTO quartermaster.infrastructure_clusters (
    cluster_id, cluster_name, cluster_type, base_url,
    cluster_class,
    max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
    is_default_cluster, is_platform_official, public_topology,
    visibility, short_description,
    cell_id, control_cell_id, region_id, eligible_serving_cell_ids, owner_tenant_id
)
VALUES (
    'demo-media-us', 'Demo Media US', 'edge', 'us.demo.frameworks.network',
    'platform_official',
    0, 0, 0,
    FALSE, TRUE, TRUE,
    'public', 'Media cluster of the second platform cell: edge nodes, stream routing, viewer delivery',
    'us-primary', 'us-primary', 'us-east', ARRAY['us-primary', 'central-primary'],
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'
)
ON CONFLICT (cluster_id) DO UPDATE SET
    cluster_class = EXCLUDED.cluster_class, is_platform_official = TRUE, is_active = TRUE,
    public_topology = TRUE, visibility = 'public',
    cell_id = 'us-primary', control_cell_id = 'us-primary', region_id = 'us-east',
    eligible_serving_cell_ids = EXCLUDED.eligible_serving_cell_ids, updated_at = NOW();

INSERT INTO quartermaster.tenant_cluster_assignments (tenant_id, cluster_id, deployment_tier, is_primary)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'us-primary', 'pro', FALSE),
       ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media-us', 'pro', FALSE)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, access_source, subscription_status, is_active
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'us-primary', 'owner', 'platform_tier', 'active', TRUE),
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media-us', 'owner', 'platform_tier', 'active', TRUE)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level, access_source = EXCLUDED.access_source,
    subscription_status = 'active', is_active = TRUE, updated_at = NOW();

UPDATE quartermaster.infrastructure_clusters
SET cell_id = 'us-primary', control_cell_id = 'us-primary', region_id = 'us-east',
    eligible_serving_cell_ids = ARRAY['us-primary', 'central-primary'],
    allow_private_pull_sources = TRUE, updated_at = NOW()
WHERE cluster_id = 'demo-selfhosted';

UPDATE quartermaster.infrastructure_clusters
SET eligible_serving_cell_ids = ARRAY['central-primary', 'us-primary'], updated_at = NOW()
WHERE cluster_id IN ('central-primary', 'demo-media');

-- Every edge enrolls with a bootstrap token: edge-node-1 into demo-media with the demo
-- token from the base seed, edge-node-b into demo-media-us and edge-node-private into
-- demo-selfhosted with the tokens below.

-- foghorn-b runs on the us-primary core node. The cell's edges are NOT pre-seeded:
-- Helmsman enrolls each with its bootstrap token, and a pre-existing row without an
-- identity binding blocks enrollment.
INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES
    ('us-node-1', 'us-primary', 'us-node-1', 'core', 'active',
     'Ashburn', '127.0.0.1', '127.0.0.1', 39.0438, -77.4874, '{"region":"us-east"}', '{}')
ON CONFLICT (node_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, status = EXCLUDED.status, region = EXCLUDED.region,
    latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude, tags = EXCLUDED.tags;

INSERT INTO quartermaster.cluster_services (cluster_id, service_id, desired_state, desired_replicas)
VALUES ('us-primary', 'foghorn', 'running', 1)
ON CONFLICT (cluster_id, service_id) DO NOTHING;

INSERT INTO quartermaster.service_instances (
    id, instance_id, cluster_id, node_id, service_id,
    protocol, advertise_host, port, status, health_status,
    metadata, started_at, created_at, updated_at
) VALUES (
    '5eedf0e1-000b-da7a-f0e1-000bda7a000b',
    'foghorn-b', 'us-primary', 'us-node-1', 'foghorn',
    'grpc', 'foghorn-b', 18019, 'running', 'unknown',
    '{"foghorn_listener": "internal_control"}', NOW(), NOW(), NOW()
)
ON CONFLICT (instance_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, node_id = EXCLUDED.node_id, advertise_host = EXCLUDED.advertise_host,
    port = EXCLUDED.port, metadata = EXCLUDED.metadata, status = 'running', health_status = 'unknown', stopped_at = NULL, updated_at = NOW();

-- foghorn-b serves its own platform cluster, the cell's platform media cluster and the
-- tenant's virtual private cluster; ListPeers resolves each one's Foghorn address from
-- these assignments.
INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
VALUES ('5eedf0e1-000b-da7a-f0e1-000bda7a000b', 'us-primary'),
       ('5eedf0e1-000b-da7a-f0e1-000bda7a000b', 'demo-media-us'),
       ('5eedf0e1-000b-da7a-f0e1-000bda7a000b', 'demo-selfhosted')
ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = TRUE, updated_at = NOW();

-- Enrollment tokens for cell B's edges: edge-node-b (edge-b) into the platform media
-- cluster, edge-node-private (edge-private) into the tenant's private cluster.
-- Plaintexts: demo_bootstrap_token_for_local_two_cell_testing_only and
-- demo_bootstrap_token_for_local_private_edge_testing_only
INSERT INTO quartermaster.bootstrap_tokens (
    id, token_hash, token_prefix, kind, name,
    tenant_id, cluster_id, expected_ip,
    metadata, usage_limit, usage_count,
    expires_at, used_at, created_by, created_at
) VALUES (
    '5eedb007-5eed-da7a-b007-5eedda7a000b',
    encode(digest('demo_bootstrap_token_for_local_two_cell_testing_only', 'sha256'), 'hex'),
    'demo_bootstr...',
    'edge_node',
    'Demo Two-Cell Edge Node Bootstrap',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'demo-media-us',
    NULL,
    '{"purpose": "two-cell-proof", "environment": "development"}',
    10, 0,
    NOW() + INTERVAL '30 days',
    NULL,
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    NOW()
), (
    '5eedb007-5eed-da7a-b007-5eedda7a000c',
    encode(digest('demo_bootstrap_token_for_local_private_edge_testing_only', 'sha256'), 'hex'),
    'demo_bootstr...',
    'edge_node',
    'Demo Private Edge Node Bootstrap',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'demo-selfhosted',
    NULL,
    '{"purpose": "private-cluster-proof", "environment": "development"}',
    10, 0,
    NOW() + INTERVAL '30 days',
    NULL,
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    NOW()
) ON CONFLICT (token_hash) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, expires_at = NOW() + INTERVAL '30 days';
