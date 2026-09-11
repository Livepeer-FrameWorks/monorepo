-- Second media cell for the two-cell media proof (docker compose profile two-cell).
-- Applied by scripts/verify-two-cell-media.sh after the ordinary demo seed; never on
-- first boot. Every Foghorn is platform-run: foghorn-b identifies as the platform-official
-- us-primary cluster (its own cell, US region), and the tenant's virtual private cluster
-- demo-selfhosted moves under that cell so foghorn-b serves it, the same way the central
-- Foghorn pair serves demo-media. Each cell may serve the other's content.

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

INSERT INTO quartermaster.tenant_cluster_assignments (tenant_id, cluster_id, deployment_tier, is_primary)
VALUES ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'us-primary', 'pro', FALSE)
ON CONFLICT (tenant_id, cluster_id) DO NOTHING;

INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, access_source, subscription_status, is_active
) VALUES
    ('5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'us-primary', 'owner', 'platform_tier', 'active', TRUE)
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

-- Both edges enroll with bootstrap tokens: edge-node-1 into demo-media with the demo
-- token from the base seed, edge-node-b into demo-selfhosted with the token below.

-- foghorn-b runs on the us-primary core node. The private edge (edge-node-b) is NOT
-- pre-seeded: Helmsman enrolls it into demo-selfhosted with the bootstrap token below,
-- and a pre-existing row without an identity binding blocks enrollment.
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
    started_at, created_at, updated_at
) VALUES (
    '5eedf0e1-000b-da7a-f0e1-000bda7a000b',
    'foghorn-b', 'us-primary', 'us-node-1', 'foghorn',
    'grpc', 'foghorn-b', 18019, 'running', 'unknown',
    NOW(), NOW(), NOW()
)
ON CONFLICT (instance_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, node_id = EXCLUDED.node_id, advertise_host = EXCLUDED.advertise_host,
    port = EXCLUDED.port, status = 'running', health_status = 'unknown', stopped_at = NULL, updated_at = NOW();

-- foghorn-b serves its own platform cluster and the tenant's virtual private cluster;
-- ListPeers resolves demo-selfhosted's Foghorn address from this assignment.
INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
VALUES ('5eedf0e1-000b-da7a-f0e1-000bda7a000b', 'us-primary'),
       ('5eedf0e1-000b-da7a-f0e1-000bda7a000b', 'demo-selfhosted')
ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = TRUE, updated_at = NOW();

-- Enrollment token for edge-node-b, bound to the demo-selfhosted cluster.
-- Plaintext: demo_bootstrap_token_for_local_two_cell_testing_only
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
    'demo-selfhosted',
    NULL,
    '{"purpose": "two-cell-proof", "environment": "development"}',
    10, 0,
    NOW() + INTERVAL '30 days',
    NULL,
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    NOW()
) ON CONFLICT (token_hash) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, expires_at = NOW() + INTERVAL '30 days';
