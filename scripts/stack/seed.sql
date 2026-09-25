-- Stack-only Quartermaster rows (applied by scripts/stack/up.sh after the demo
-- and two-cell seeds). Registers what production registers through the CLI:
--   * one physical Livepeer gateway per cell, discoverable the way Foghorn's
--     broadcaster fanout requires: running + healthy + fresh (the health poller
--     probes http://<proxy>/healthz), on an active node with an external IP, and
--     covered by a desired physical ingress site for its infra FQDN;
--   * the second cell-B Foghorn replica (foghorn-b-2).
-- Seeded before those services start: Foghorn loads its served clusters at boot.

INSERT INTO quartermaster.infrastructure_nodes (
    node_id, cluster_id, node_name, node_type, status,
    region, external_ip, internal_ip, latitude, longitude, tags, metadata
) VALUES
    ('stack-lp-a', 'central-primary', 'stack-lp-a', 'core', 'active',
     'Amsterdam', '127.0.0.1', '127.0.0.1', 52.3676, 4.9041, '{}', '{"stack":"livepeer"}'),
    ('stack-lp-b', 'us-primary', 'stack-lp-b', 'core', 'active',
     'Ashburn', '127.0.0.1', '127.0.0.1', 39.0438, -77.4874, '{}', '{"stack":"livepeer"}')
ON CONFLICT (node_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, status = 'active', external_ip = EXCLUDED.external_ip;

INSERT INTO quartermaster.service_instances (
    id, instance_id, cluster_id, node_id, service_id,
    protocol, advertise_host, port, health_endpoint_override, status, health_status,
    started_at, created_at, updated_at
) VALUES
    ('5eedf0e1-00a1-da7a-f0e1-00a1da7a00a1', 'stack-livepeer-gateway-a', 'central-primary', 'stack-lp-a',
     'livepeer-gateway', 'http', 'livepeer-proxy-a', 80, '/healthz', 'running', 'unknown', NOW(), NOW(), NOW()),
    ('5eedf0e1-00b1-da7a-f0e1-00b1da7a00b1', 'stack-livepeer-gateway-b', 'us-primary', 'stack-lp-b',
     'livepeer-gateway', 'http', 'livepeer-proxy-b', 80, '/healthz', 'running', 'unknown', NOW(), NOW(), NOW()),
    ('5eedf0e1-000c-da7a-f0e1-000cda7a000c', 'foghorn-b-2', 'us-primary', 'us-node-1',
     'foghorn', 'grpc', 'foghorn-b-2', 18019, NULL, 'running', 'unknown', NOW(), NOW(), NOW())
ON CONFLICT (instance_id) DO UPDATE SET
    cluster_id = EXCLUDED.cluster_id, node_id = EXCLUDED.node_id, advertise_host = EXCLUDED.advertise_host,
    port = EXCLUDED.port, health_endpoint_override = EXCLUDED.health_endpoint_override,
    status = 'running', stopped_at = NULL, updated_at = NOW();

-- Each gateway serves its cell's platform and media clusters; Foghorn asks for
-- the stream's origin cluster first, then its own cluster.
INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id) VALUES
    ('5eedf0e1-00a1-da7a-f0e1-00a1da7a00a1', 'central-primary'),
    ('5eedf0e1-00a1-da7a-f0e1-00a1da7a00a1', 'demo-media'),
    ('5eedf0e1-00b1-da7a-f0e1-00b1da7a00b1', 'us-primary'),
    ('5eedf0e1-00b1-da7a-f0e1-00b1da7a00b1', 'demo-selfhosted'),
    ('5eedf0e1-000c-da7a-f0e1-000cda7a000c', 'us-primary'),
    ('5eedf0e1-000c-da7a-f0e1-000cda7a000c', 'demo-selfhosted')
ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = TRUE, updated_at = NOW();

INSERT INTO quartermaster.tls_bundles (bundle_id, cluster_id, domains, issuer, email, metadata) VALUES
    ('stack-livepeer-gateway-a', 'central-primary', '["livepeer-gateway.stack-lp-a.infra.frameworks.network"]', 'navigator', 'stack@frameworks.network', '{}'),
    ('stack-livepeer-gateway-b', 'us-primary', '["livepeer-gateway.stack-lp-b.infra.frameworks.network"]', 'navigator', 'stack@frameworks.network', '{}')
ON CONFLICT (bundle_id) DO UPDATE SET domains = EXCLUDED.domains;

INSERT INTO quartermaster.ingress_sites (site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream) VALUES
    ('stack-livepeer-gateway-a', 'central-primary', 'stack-lp-a',
     '["livepeer-gateway.stack-lp-a.infra.frameworks.network"]', 'stack-livepeer-gateway-a', 'physical', 'livepeer-gateway-a:8935'),
    ('stack-livepeer-gateway-b', 'us-primary', 'stack-lp-b',
     '["livepeer-gateway.stack-lp-b.infra.frameworks.network"]', 'stack-livepeer-gateway-b', 'physical', 'livepeer-gateway-b:8935')
ON CONFLICT (site_id) DO UPDATE SET domains = EXCLUDED.domains, kind = EXCLUDED.kind, upstream = EXCLUDED.upstream;
