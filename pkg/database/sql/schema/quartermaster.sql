-- ============================================================================
-- QUARTERMASTER SCHEMA - TENANT & INFRASTRUCTURE REGISTRY
-- ============================================================================
-- Manages tenants, infrastructure clusters, nodes, services, and orchestration
-- Core registry for multi-tenant infrastructure deployment and management
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS quartermaster;

-- ============================================================================
-- EXTENSIONS & TYPES
-- ============================================================================

-- Required extensions for UUID generation and cryptographic functions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- TENANT MANAGEMENT
-- ============================================================================

-- Core tenant registry with branding, limits, and deployment configuration
CREATE TABLE IF NOT EXISTS quartermaster.tenants (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    subdomain VARCHAR(100) UNIQUE,
    custom_domain VARCHAR(255),

    -- ===== BRANDING =====
    logo_url VARCHAR(500),
    primary_color VARCHAR(7) DEFAULT '#6366f1',
    secondary_color VARCHAR(7) DEFAULT '#f59e0b',

    -- ===== DEPLOYMENT CONFIGURATION =====
    deployment_tier VARCHAR(50) DEFAULT 'free',  -- billing-derived; Purser stamps billing_tiers.tier_name
    deployment_model VARCHAR(50) DEFAULT 'shared',
    primary_cluster_id VARCHAR(100),
    official_cluster_id VARCHAR(100),  -- billing-tier cluster providing geographic coverage

    -- ===== INFRASTRUCTURE CONNECTIVITY =====
    kafka_topic_prefix VARCHAR(100),
    kafka_brokers TEXT[],
    database_url TEXT,

    -- ===== API RATE LIMITING =====
    rate_limit_per_minute INTEGER NOT NULL DEFAULT 100,   -- Requests per minute
    rate_limit_burst INTEGER NOT NULL DEFAULT 20,         -- Burst allowance above limit

    -- ===== OWNERSHIP LIMITS =====
    max_owned_clusters INTEGER DEFAULT 1,

    -- ===== STATUS & LIFECYCLE =====
    is_provider BOOLEAN DEFAULT FALSE,
    is_active BOOLEAN DEFAULT TRUE,
    -- Tenant-wide master switch for Skipper AI monitoring/notifications. When
    -- false, Skipper skips the tenant entirely regardless of tier entitlement
    -- or per-stream overrides. Default TRUE preserves existing behavior.
    monitoring_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    trial_ends_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- TENANT ATTRIBUTION
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.tenant_attribution (
    tenant_id UUID PRIMARY KEY REFERENCES quartermaster.tenants(id) ON DELETE CASCADE,
    signup_channel VARCHAR(50) NOT NULL,
    signup_method VARCHAR(50),
    utm_source VARCHAR(255),
    utm_medium VARCHAR(100),
    utm_campaign VARCHAR(255),
    utm_content VARCHAR(255),
    utm_term VARCHAR(255),
    http_referer TEXT,
    landing_page TEXT,
    referral_code VARCHAR(100),
    is_agent BOOLEAN DEFAULT FALSE,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_attribution_channel
    ON quartermaster.tenant_attribution (signup_channel, created_at);

CREATE INDEX IF NOT EXISTS idx_tenant_attribution_utm
    ON quartermaster.tenant_attribution (utm_source)
    WHERE utm_source IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tenant_attribution_referral
    ON quartermaster.tenant_attribution (referral_code)
    WHERE referral_code IS NOT NULL;

-- ============================================================================
-- BOOTSTRAP TENANT ALIASES
-- ============================================================================
-- Stable, operator-readable handle the bootstrap-desired-state file uses to
-- reference tenants without knowing the DB UUID. `quartermaster bootstrap`
-- writes this row in the same transaction it creates a tenant; subsequent
-- bootstrap runs resolve aliases through this table to find the same tenant.
-- The `frameworks` alias is reserved for the system tenant.
-- ============================================================================
CREATE TABLE IF NOT EXISTS quartermaster.bootstrap_tenant_aliases (
    alias       TEXT PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES quartermaster.tenants(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_bootstrap_tenant_alias_format CHECK (alias ~ '^[a-z][a-z0-9-]{0,63}$')
);

CREATE TABLE IF NOT EXISTS quartermaster.referral_codes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code VARCHAR(100) NOT NULL UNIQUE,
    owner_tenant_id UUID REFERENCES quartermaster.tenants(id),
    partner_name VARCHAR(255),
    is_active BOOLEAN DEFAULT TRUE,
    max_uses INTEGER,
    current_uses INTEGER DEFAULT 0,
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW()
);


-- ============================================================================
-- INFRASTRUCTURE CLUSTERS
-- ============================================================================

-- Physical/logical cluster registry with capacity and connectivity
CREATE TABLE IF NOT EXISTS quartermaster.infrastructure_clusters (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_name VARCHAR(255) NOT NULL,
    cluster_type VARCHAR(50) NOT NULL,
    
    -- ===== OWNERSHIP & DEPLOYMENT =====
    owner_tenant_id UUID REFERENCES quartermaster.tenants(id),
    deployment_model VARCHAR(50) DEFAULT 'shared',

    -- ===== CONNECTIVITY =====
    base_url VARCHAR(500) NOT NULL,
    database_url TEXT,
    periscope_url TEXT,
    kafka_brokers TEXT[],

    -- ===== REGION, CELL & CLASS =====
    -- Geographic region this cluster lives in (e.g. "eu-west", "us-east",
    -- "ap-tokyo"). Used by resolver scoring + GeoIP-based viewer/ingest
    -- routing and by event envelope source_region stamping.
    region_id VARCHAR(50),
    -- Failure-isolation cell this cluster belongs to. Defaults to cluster_id
    -- (every cluster is its own cell today); multi-cluster cells are reserved
    -- for ops grouping later. Foghorn HA boundary is per-cell, not per-cluster.
    cell_id VARCHAR(100),
    -- Coarse ownership/billing classification driving plan-tier filter at the
    -- resolver: platform_official | tenant_private | third_party_marketplace.
    -- Free tenants are admitted only to platform_official; premium adds
    -- third_party_marketplace; enterprise adds tenant_private. Self-hosted is
    -- expressed via explicit tenant_cluster_access grants regardless of class.
    cluster_class VARCHAR(50),

    -- ===== CAPACITY LIMITS =====
    max_concurrent_streams INTEGER DEFAULT 0,
    max_concurrent_viewers INTEGER DEFAULT 0,
    max_bandwidth_mbps INTEGER DEFAULT 0,

    -- ===== STATUS & HEALTH =====
    is_active BOOLEAN DEFAULT TRUE,
    is_default_cluster BOOLEAN DEFAULT FALSE,
    is_platform_official BOOLEAN DEFAULT FALSE,
    public_topology BOOLEAN NOT NULL DEFAULT FALSE,
    -- Pull-source private-network allowance: when TRUE, pull streams placed on
    -- this cluster may resolve from RFC1918 / multicast literals. Defaults
    -- FALSE so platform-official clusters reject tenant-private upstreams.
    -- Set only on self-hosted clusters whose edges legitimately pull from
    -- LAN/VPC sources reachable from those edges. Pulled by the CLI render
    -- path when validating pull-stream placement.
    allow_private_pull_sources BOOLEAN NOT NULL DEFAULT FALSE,
    health_status VARCHAR(50) DEFAULT 'healthy',
    last_seen TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    -- ===== S3 STORAGE CONFIGURATION =====
    s3_bucket VARCHAR(255),
    s3_endpoint VARCHAR(500),
    s3_region VARCHAR(50),

    -- ===== WIREGUARD MESH CONFIGURATION =====
    -- IPv4 CIDR for the cluster's WireGuard mesh. Used by
    -- BootstrapInfrastructureNode to allocate mesh IPs for enrolling nodes.
    -- Sourced from the manifest's wireguard.mesh_cidr during cluster provision.
    wg_mesh_cidr VARCHAR(43),
    -- Default WireGuard UDP listen port assigned to enrolling nodes when the
    -- bootstrap request omits it. Sourced from wireguard.listen_port.
    wg_listen_port INTEGER,

    -- ===== MARKETPLACE CONFIGURATION =====
    visibility VARCHAR(20) DEFAULT 'private',
    pricing_model VARCHAR(20) DEFAULT 'free_unmetered',
    monthly_price_cents INTEGER DEFAULT 0,
    metered_rate_config JSONB DEFAULT '{}',
    requires_approval BOOLEAN DEFAULT FALSE,
    short_description VARCHAR(500),

    -- ===== VIRTUALFOGHORN CONTROL CELL =====
    -- The regional Foghorn cell that owns Helmsman ConfigSeed distribution,
    -- tenant alias TLS bundle distribution, and edge apply-state ACK for this
    -- cluster. For platform_official clusters this equals cell_id (self-
    -- control). For tenant_private / third_party_marketplace / self-hosted
    -- clusters, it points to the regional cell assigned at creation time.
    -- NULL until assignment runs. Navigator and resolvers consult this when
    -- choosing which Foghorn to ask for apply-state.
    control_cell_id VARCHAR(100),
    -- Foghorn cells authorized to serve content from this cluster. Defaults
    -- to ARRAY[control_cell_id]; populated explicitly when multi-cell serving
    -- is opted-in per cluster.
    eligible_serving_cell_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    -- NULL during steady state; 'draining' while an operator-initiated
    -- ReassignClusterControlCell drains the old cell and waits for the new
    -- cell's GetEdgeApplyState ACK. Navigator filters tenant alias publication
    -- against this.
    reassignment_state VARCHAR(20),

    -- ===== CONSTRAINTS =====
    CONSTRAINT chk_cluster_visibility CHECK (visibility IN ('public', 'unlisted', 'private')),
    CONSTRAINT chk_cluster_pricing_model CHECK (pricing_model IN ('free_unmetered', 'metered', 'monthly', 'custom', 'tier_inherit')),
    CONSTRAINT chk_cluster_class CHECK (cluster_class IS NULL OR cluster_class IN ('platform_official', 'tenant_private', 'third_party_marketplace')),
    CONSTRAINT chk_cluster_reassignment_state CHECK (reassignment_state IS NULL OR reassignment_state IN ('draining'))
);

-- ============================================================================
-- INFRASTRUCTURE NODES
-- ============================================================================

-- Physical nodes within clusters with networking, resources, and capabilities
CREATE TABLE IF NOT EXISTS quartermaster.infrastructure_nodes (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id),
    node_name VARCHAR(255) NOT NULL,
    node_type VARCHAR(50) NOT NULL,
    status VARCHAR(50) DEFAULT 'offline',

    -- ===== NETWORKING =====
    internal_ip INET,
    external_ip INET,
    wireguard_ip INET,
    wireguard_public_key TEXT,
    wireguard_listen_port INTEGER DEFAULT 51820,

    -- ===== ENROLLMENT PROVENANCE =====
    -- How this row came to be. Governs audit/reconcile semantics and who
    -- owns the node's WireGuard private key:
    --   gitops_seed      — declared in cluster.yaml, private key in SOPS,
    --                      cold-boot capable.
    --   runtime_enrolled — joined via a bootstrap token after the cluster
    --                      was alive; private key lives on the node.
    --   adopted_local    — runtime-enrolled node whose public identity is
    --                      now in GitOps; Ansible preserves the on-disk key.
    enrollment_origin VARCHAR(32) NOT NULL DEFAULT 'gitops_seed'
        CHECK (enrollment_origin IN ('gitops_seed', 'runtime_enrolled', 'adopted_local')),

    region VARCHAR(50),
    availability_zone VARCHAR(50),
    latitude DOUBLE PRECISION,
    longitude DOUBLE PRECISION,

    -- ===== HARDWARE RESOURCES =====
    cpu_cores INTEGER,
    memory_gb INTEGER,
    disk_gb INTEGER,

    -- ===== HEARTBEAT =====
    last_heartbeat TIMESTAMP,

    -- ===== LATEST RESOURCE SNAPSHOT =====
    -- Reported by Privateer on every SyncMesh. snapshot_at is Quartermaster
    -- receipt time so freshness does not depend on node clock skew. NULL means
    -- the agent has never sent a complete snapshot (older client, fresh row, or
    -- collection failure on every sync). Bridge synthesises
    -- InfrastructureNode.liveState from these columns for core nodes.
    snapshot_cpu_percent REAL,
    snapshot_ram_used_bytes BIGINT,
    snapshot_ram_total_bytes BIGINT,
    snapshot_disk_used_bytes BIGINT,
    snapshot_disk_total_bytes BIGINT,
    snapshot_uptime_seconds BIGINT,
    snapshot_at TIMESTAMPTZ,

    -- ===== APPLIED MESH REVISION =====
    -- Last mesh_revision the Privateer agent reported it had applied via
    -- SyncMesh. Used by 'mesh wg audit' / 'mesh status' to detect agents
    -- running stale managed configs. NULL for nodes that have never
    -- reported a revision (older clients, fresh rows, or agents stuck
    -- before their first managed apply).
    applied_mesh_revision TEXT,

    -- ===== METADATA & CONFIGURATION =====
    tags JSONB DEFAULT '{}',
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    CONSTRAINT uq_qm_infrastructure_nodes_node_cluster UNIQUE (node_id, cluster_id)
);

-- ============================================================================
-- SERVICE CATALOG & ORCHESTRATION
-- ============================================================================

-- Master catalog of all microservices and their deployment specifications
CREATE TABLE IF NOT EXISTS quartermaster.services (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id VARCHAR(100) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    plane VARCHAR(50) NOT NULL, -- control, data, edge
    type VARCHAR(100),          -- Service type for discovery
    protocol VARCHAR(10) DEFAULT 'http',

    -- ===== SERVICE DEFINITION =====
    description TEXT,
    default_port INTEGER,
    health_check_path VARCHAR(255),
    
    -- ===== DEPLOYMENT SPECIFICATION =====
    docker_image VARCHAR(255),
    version VARCHAR(50),
    dependencies TEXT[],
    
    -- ===== METADATA =====
    tags JSONB DEFAULT '{}',
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Service deployments per cluster with desired state and configuration
CREATE TABLE IF NOT EXISTS quartermaster.cluster_services (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id),
    service_id VARCHAR(100) NOT NULL REFERENCES quartermaster.services(service_id),
    
    -- ===== DESIRED STATE =====
    desired_state VARCHAR(50) DEFAULT 'running',
    desired_replicas INTEGER DEFAULT 1,
    current_replicas INTEGER DEFAULT 0,
    
    -- ===== CONFIGURATION =====
    config_blob JSONB DEFAULT '{}',
    environment_vars JSONB DEFAULT '{}',
    
    -- ===== RESOURCE LIMITS =====
    cpu_limit DECIMAL(4,2),
    memory_limit_mb INTEGER,
    
    -- ===== STATUS =====
    health_status VARCHAR(50) DEFAULT 'unknown',
    last_deployed TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(cluster_id, service_id)
);

-- Individual service instances running on specific nodes
CREATE TABLE IF NOT EXISTS quartermaster.service_instances (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id),
    node_id VARCHAR(100) REFERENCES quartermaster.infrastructure_nodes(node_id),
    service_id VARCHAR(100) NOT NULL REFERENCES quartermaster.services(service_id),
    
    -- ===== DEPLOYMENT INFO =====
    protocol VARCHAR(10) DEFAULT 'http',
    advertise_host VARCHAR(255),
    health_endpoint_override VARCHAR(255),
    version VARCHAR(50),
    port INTEGER,
    process_id INTEGER,
    container_id VARCHAR(255),
    
    -- ===== STATUS & HEALTH =====
    status VARCHAR(50) DEFAULT 'starting',
    health_status VARCHAR(50) DEFAULT 'unknown',
    started_at TIMESTAMP,
    stopped_at TIMESTAMP,
    last_health_check TIMESTAMP,
    metadata JSONB DEFAULT '{}',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT fk_qm_service_instances_node_cluster
        FOREIGN KEY (node_id, cluster_id)
        REFERENCES quartermaster.infrastructure_nodes(node_id, cluster_id)
        DEFERRABLE INITIALLY IMMEDIATE
);

ALTER TABLE IF EXISTS quartermaster.service_instances
    ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';

ALTER TABLE IF EXISTS quartermaster.infrastructure_nodes
    ADD COLUMN IF NOT EXISTS applied_mesh_revision TEXT;

-- ============================================================================
-- SERVICE-CLUSTER ASSIGNMENTS (many-to-many, generic over service type)
-- ============================================================================

-- Maps service instances to the logical clusters they publish/discover under.
-- Used for cluster-scoped Bunny services (foghorn, chandler, livepeer-gateway):
-- a single instance pool can serve N logical media clusters, and one media
-- cluster can have N instances. service_instances.cluster_id remains the
-- physical/runtime cluster of the process (FK-bound to its node); this table
-- carries the logical media-cluster identity used by DNS and DiscoverServices.
-- source carries provenance, mirroring infrastructure_nodes.enrollment_origin:
--   'gitops_seed'   — written by GitOps seeding that owns the row
--   'runtime'       — written by AssignServiceToCluster / EnableSelfHosting at runtime
--   'adopted_local' — runtime row that has been adopted into GitOps ownership
-- Ordinary runtime upserts preserve the existing source on conflict; only
-- explicit adopt/unmanage operations flip provenance. Default 'runtime'
-- backfills correctly because every existing row was written by a runtime RPC.
CREATE TABLE IF NOT EXISTS quartermaster.service_cluster_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_instance_id UUID NOT NULL REFERENCES quartermaster.service_instances(id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    source VARCHAR(32) NOT NULL DEFAULT 'runtime' CHECK (source IN ('gitops_seed', 'runtime', 'adopted_local')),
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE(service_instance_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_qm_sca_cluster ON quartermaster.service_cluster_assignments(cluster_id) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_qm_sca_instance ON quartermaster.service_cluster_assignments(service_instance_id);

-- ============================================================================
-- WIREGUARD MESH READ MODEL
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.mesh_topology_state (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    revision BIGINT NOT NULL DEFAULT 1,
    warmed_revision BIGINT NOT NULL DEFAULT 0,
    warmed_planner_version TEXT NOT NULL DEFAULT '',
    warming_started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE IF EXISTS quartermaster.mesh_topology_state
    ADD COLUMN IF NOT EXISTS warmed_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS warmed_planner_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS warming_started_at TIMESTAMPTZ;

INSERT INTO quartermaster.mesh_topology_state (id, revision)
VALUES (TRUE, 1)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS quartermaster.mesh_node_configs (
    node_id VARCHAR(100) PRIMARY KEY REFERENCES quartermaster.infrastructure_nodes(node_id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    mesh_revision TEXT NOT NULL,
    topology_source_hash TEXT NOT NULL,
    wireguard_ip INET NOT NULL,
    wireguard_port INTEGER NOT NULL,
    peers JSONB NOT NULL DEFAULT '[]'::jsonb,
    service_endpoints JSONB NOT NULL DEFAULT '{}'::jsonb,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_qm_mesh_node_configs_peers_array CHECK (jsonb_typeof(peers) = 'array'),
    CONSTRAINT chk_qm_mesh_node_configs_service_endpoints_object CHECK (jsonb_typeof(service_endpoints) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_qm_mesh_node_configs_cluster ON quartermaster.mesh_node_configs(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_mesh_node_configs_revision ON quartermaster.mesh_node_configs(mesh_revision);

CREATE OR REPLACE FUNCTION quartermaster.bump_mesh_topology_state()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO quartermaster.mesh_topology_state (id, revision, updated_at)
    VALUES (TRUE, 1, NOW())
    ON CONFLICT (id)
    DO UPDATE SET revision = quartermaster.mesh_topology_state.revision + 1,
                  updated_at = NOW();

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_nodes_insert_delete ON quartermaster.infrastructure_nodes;
CREATE TRIGGER trg_qm_mesh_topology_nodes_insert_delete
AFTER INSERT OR DELETE ON quartermaster.infrastructure_nodes
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_nodes_update ON quartermaster.infrastructure_nodes;
CREATE TRIGGER trg_qm_mesh_topology_nodes_update
AFTER UPDATE OF cluster_id, node_name, node_type, status, internal_ip, external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port, metadata ON quartermaster.infrastructure_nodes
FOR EACH ROW
WHEN (
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.node_name IS DISTINCT FROM NEW.node_name OR
    OLD.node_type IS DISTINCT FROM NEW.node_type OR
    OLD.status IS DISTINCT FROM NEW.status OR
    OLD.internal_ip IS DISTINCT FROM NEW.internal_ip OR
    OLD.external_ip IS DISTINCT FROM NEW.external_ip OR
    OLD.wireguard_ip IS DISTINCT FROM NEW.wireguard_ip OR
    OLD.wireguard_public_key IS DISTINCT FROM NEW.wireguard_public_key OR
    OLD.wireguard_listen_port IS DISTINCT FROM NEW.wireguard_listen_port OR
    OLD.metadata->'desired_service_types' IS DISTINCT FROM NEW.metadata->'desired_service_types' OR
    OLD.metadata->'service_types' IS DISTINCT FROM NEW.metadata->'service_types' OR
    OLD.metadata->'desired_cluster_ids' IS DISTINCT FROM NEW.metadata->'desired_cluster_ids' OR
    OLD.metadata->'service_cluster_ids' IS DISTINCT FROM NEW.metadata->'service_cluster_ids' OR
    OLD.metadata->'logical_cluster_ids' IS DISTINCT FROM NEW.metadata->'logical_cluster_ids'
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_services_insert_delete ON quartermaster.services;
CREATE TRIGGER trg_qm_mesh_topology_services_insert_delete
AFTER INSERT OR DELETE ON quartermaster.services
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_services_update ON quartermaster.services;
CREATE TRIGGER trg_qm_mesh_topology_services_update
AFTER UPDATE OF type, plane ON quartermaster.services
FOR EACH ROW
WHEN (
    OLD.type IS DISTINCT FROM NEW.type OR
    OLD.plane IS DISTINCT FROM NEW.plane
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_instances_insert_delete ON quartermaster.service_instances;
CREATE TRIGGER trg_qm_mesh_topology_service_instances_insert_delete
AFTER INSERT OR DELETE ON quartermaster.service_instances
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_instances_update ON quartermaster.service_instances;
CREATE TRIGGER trg_qm_mesh_topology_service_instances_update
AFTER UPDATE OF cluster_id, node_id, service_id, status, metadata ON quartermaster.service_instances
FOR EACH ROW
WHEN (
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.node_id IS DISTINCT FROM NEW.node_id OR
    OLD.service_id IS DISTINCT FROM NEW.service_id OR
    OLD.status IS DISTINCT FROM NEW.status OR
    OLD.metadata->'infra_role' IS DISTINCT FROM NEW.metadata->'infra_role' OR
    OLD.metadata->'infra_name' IS DISTINCT FROM NEW.metadata->'infra_name'
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments ON quartermaster.service_cluster_assignments;
DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments_insert_delete ON quartermaster.service_cluster_assignments;
CREATE TRIGGER trg_qm_mesh_topology_service_assignments_insert_delete
AFTER INSERT OR DELETE ON quartermaster.service_cluster_assignments
FOR EACH ROW EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

DROP TRIGGER IF EXISTS trg_qm_mesh_topology_service_assignments_update ON quartermaster.service_cluster_assignments;
CREATE TRIGGER trg_qm_mesh_topology_service_assignments_update
AFTER UPDATE OF service_instance_id, cluster_id, is_active ON quartermaster.service_cluster_assignments
FOR EACH ROW
WHEN (
    OLD.service_instance_id IS DISTINCT FROM NEW.service_instance_id OR
    OLD.cluster_id IS DISTINCT FROM NEW.cluster_id OR
    OLD.is_active IS DISTINCT FROM NEW.is_active
)
EXECUTE FUNCTION quartermaster.bump_mesh_topology_state();

-- ============================================================================
-- CORE INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_clusters_cluster_type ON quartermaster.infrastructure_clusters(cluster_type);
CREATE INDEX IF NOT EXISTS idx_qm_clusters_platform_official ON quartermaster.infrastructure_clusters(is_platform_official) WHERE is_platform_official = true;
CREATE INDEX IF NOT EXISTS idx_qm_clusters_public_topology ON quartermaster.infrastructure_clusters(public_topology) WHERE public_topology = true;
CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_clusters_owner_tenant ON quartermaster.infrastructure_clusters(owner_tenant_id);
CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_nodes_cluster_id ON quartermaster.infrastructure_nodes(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_services_plane ON quartermaster.services(plane);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_services_cluster_id ON quartermaster.cluster_services(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_service_instances_cluster_id ON quartermaster.service_instances(cluster_id);

-- ============================================================================
-- INGRESS & TLS DESIRED STATE
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.tls_bundles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bundle_id VARCHAR(200) NOT NULL UNIQUE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    domains JSONB NOT NULL DEFAULT '[]'::jsonb,
    issuer VARCHAR(50) NOT NULL DEFAULT 'navigator',
    email TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_qm_tls_bundles_cluster_id ON quartermaster.tls_bundles(cluster_id);

CREATE TABLE IF NOT EXISTS quartermaster.ingress_sites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id VARCHAR(200) NOT NULL UNIQUE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    node_id VARCHAR(100) NOT NULL,
    domains JSONB NOT NULL DEFAULT '[]'::jsonb,
    tls_bundle_id VARCHAR(200) NOT NULL REFERENCES quartermaster.tls_bundles(bundle_id) ON DELETE RESTRICT,
    kind VARCHAR(50) NOT NULL,
    upstream TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    CONSTRAINT fk_qm_ingress_sites_node_cluster
        FOREIGN KEY (node_id, cluster_id)
        REFERENCES quartermaster.infrastructure_nodes(node_id, cluster_id)
        DEFERRABLE INITIALLY IMMEDIATE
);

CREATE INDEX IF NOT EXISTS idx_qm_ingress_sites_cluster_id ON quartermaster.ingress_sites(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_ingress_sites_node_id ON quartermaster.ingress_sites(node_id);

-- ============================================================================
-- TENANT-CLUSTER MAPPING & ACCESS CONTROL
-- ============================================================================

-- Tenant assignments to specific clusters with resource allocation
CREATE TABLE IF NOT EXISTS quartermaster.tenant_cluster_assignments (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES quartermaster.tenants(id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id),

    -- ===== ASSIGNMENT CONFIGURATION =====
    deployment_tier VARCHAR(50),
    priority INTEGER DEFAULT 1,
    is_primary BOOLEAN DEFAULT FALSE,
    is_active BOOLEAN DEFAULT TRUE,
    kafka_topic_prefix VARCHAR(100),

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, cluster_id)
);

-- Runtime access control per tenant-cluster pair
CREATE TABLE IF NOT EXISTS quartermaster.tenant_cluster_access (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,

    -- ===== ACCESS CONTROL =====
    access_level VARCHAR(50) DEFAULT 'shared', -- shared, dedicated, priority

    -- ===== RESOURCE LIMITS (Quartermaster-owned capacity enforcement) =====
    resource_limits JSONB DEFAULT '{}',  -- Tenant-specific limits: {max_streams, max_viewers, max_bandwidth_mbps}

    -- ===== SUBSCRIPTION STATUS (Approval workflow) =====
    subscription_status VARCHAR(20) DEFAULT 'active',
    requested_at TIMESTAMP,
    approved_at TIMESTAMP,
    approved_by UUID,
    rejection_reason TEXT,
    invite_token VARCHAR(128),

    -- ===== STATUS =====
    is_active BOOLEAN DEFAULT true,
    granted_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, cluster_id),

    -- ===== CONSTRAINTS =====
    CONSTRAINT chk_subscription_status CHECK (subscription_status IN ('pending_approval', 'active', 'suspended', 'rejected'))
);

-- ============================================================================
-- TENANT-CLUSTER ACCESS INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_qm_tenant_cluster_assignments_tenant ON quartermaster.tenant_cluster_assignments(tenant_id);
CREATE INDEX IF NOT EXISTS idx_qm_tenant_cluster_assignments_cluster ON quartermaster.tenant_cluster_assignments(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_tenant_cluster_access_tenant ON quartermaster.tenant_cluster_access(tenant_id);
CREATE INDEX IF NOT EXISTS idx_qm_tenant_cluster_access_cluster ON quartermaster.tenant_cluster_access(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_tenant_cluster_access_active ON quartermaster.tenant_cluster_access(is_active);

-- ============================================================================
-- EDGE RELEASE TARGETING
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.edge_releases (
    channel TEXT NOT NULL,
    version TEXT NOT NULL,
    components JSONB NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel, version),
    CONSTRAINT chk_qm_edge_release_channel CHECK (channel IN ('stable', 'rc')),
    CONSTRAINT chk_qm_edge_release_components_object CHECK (jsonb_typeof(components) = 'object')
);

CREATE TABLE IF NOT EXISTS quartermaster.cluster_release_targets (
    cluster_id VARCHAR(100) PRIMARY KEY REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    channel TEXT NOT NULL,
    target_version TEXT,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    rollout_plan JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_qm_cluster_release_target_channel CHECK (channel IN ('stable', 'rc')),
    CONSTRAINT chk_qm_cluster_release_rollout_plan_object CHECK (jsonb_typeof(rollout_plan) = 'object')
);

DELETE FROM quartermaster.cluster_release_targets WHERE channel = 'edge';
DELETE FROM quartermaster.edge_releases WHERE channel = 'edge';

ALTER TABLE quartermaster.edge_releases
    DROP COLUMN IF EXISTS metadata,
    DROP CONSTRAINT IF EXISTS chk_qm_edge_release_channel,
    ADD CONSTRAINT chk_qm_edge_release_channel CHECK (channel IN ('stable', 'rc'));

ALTER TABLE quartermaster.cluster_release_targets
    ADD COLUMN IF NOT EXISTS paused BOOLEAN NOT NULL DEFAULT FALSE,
    DROP COLUMN IF EXISTS policy,
    DROP CONSTRAINT IF EXISTS chk_qm_cluster_release_target_channel,
    ADD CONSTRAINT chk_qm_cluster_release_target_channel CHECK (channel IN ('stable', 'rc'));

DROP INDEX IF EXISTS quartermaster.idx_qm_cluster_release_targets_policy;
CREATE INDEX IF NOT EXISTS idx_qm_edge_releases_published ON quartermaster.edge_releases(channel, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_release_targets_paused ON quartermaster.cluster_release_targets(paused);

-- ============================================================================
-- UTILITY FUNCTIONS
-- ============================================================================

-- Generate random alphanumeric strings for keys and tokens (uses pgcrypto for CSPRNG)
CREATE OR REPLACE FUNCTION quartermaster.generate_random_string(length INTEGER) RETURNS TEXT AS $$
DECLARE
    chars TEXT := 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    chars_len INTEGER := 62;
    rand_bytes BYTEA;
    result TEXT := '';
    i INTEGER := 0;
BEGIN
    rand_bytes := gen_random_bytes(length);
    FOR i IN 0..length-1 LOOP
        result := result || substr(chars, (get_byte(rand_bytes, i) % chars_len) + 1, 1);
    END LOOP;
    RETURN result;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- NODE FINGERPRINTS (stable identity mapping)
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.node_fingerprints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID REFERENCES quartermaster.tenants(id),
    node_id VARCHAR(100) UNIQUE NOT NULL,
    fingerprint_machine_sha256 TEXT,
    fingerprint_macs_sha256 TEXT,
    seen_ips INET[] DEFAULT '{}',
    attrs JSONB DEFAULT '{}',
    first_seen TIMESTAMP DEFAULT NOW(),
    last_seen TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_qm_fingerprints_machine ON quartermaster.node_fingerprints(fingerprint_machine_sha256);
CREATE INDEX IF NOT EXISTS idx_qm_fingerprints_macs ON quartermaster.node_fingerprints(fingerprint_macs_sha256);

-- ============================================================================
-- BOOTSTRAP TOKENS (one-use, short-lived)
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.bootstrap_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash VARCHAR(64) UNIQUE NOT NULL,  -- SHA-256 hex digest
    token_prefix VARCHAR(20) NOT NULL DEFAULT '',  -- Display prefix (e.g. "bt_a1b2c3...")
    -- Scope and intended usage
    kind VARCHAR(32) NOT NULL, -- 'edge_node' | 'service' | 'infrastructure_node'
    name TEXT NOT NULL DEFAULT 'Bootstrap Token',
    tenant_id UUID,            -- optional; required for edge_node
    cluster_id VARCHAR(100),   -- optional; for service bootstrap in provider clusters
    expected_ip INET,          -- optional hint
    metadata JSONB DEFAULT '{}',
    usage_limit INTEGER,
    usage_count INTEGER NOT NULL DEFAULT 0,
    -- Lifecycle
    expires_at TIMESTAMP NOT NULL,
    used_at TIMESTAMP,
    created_by UUID,
    created_at TIMESTAMP DEFAULT NOW(),
    CONSTRAINT chk_kind CHECK (kind IN ('edge_node','service','infrastructure_node'))
);

CREATE INDEX IF NOT EXISTS idx_qm_bootstrap_tokens_hash ON quartermaster.bootstrap_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_qm_bootstrap_tokens_kind ON quartermaster.bootstrap_tokens(kind);

-- ============================================================================
-- MARKETPLACE INDEXES
-- ============================================================================

-- Marketplace indexes for cluster discovery
CREATE INDEX IF NOT EXISTS idx_qm_clusters_visibility ON quartermaster.infrastructure_clusters(visibility) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_qm_clusters_pricing_model ON quartermaster.infrastructure_clusters(pricing_model) WHERE is_active = true;

-- Index for pending approval queries
CREATE INDEX IF NOT EXISTS idx_qm_tca_pending_approval ON quartermaster.tenant_cluster_access(subscription_status)
    WHERE subscription_status = 'pending_approval';

-- ============================================================================
-- CLUSTER INVITES (Tenant-to-tenant invitation system)
-- ============================================================================

CREATE TABLE IF NOT EXISTS quartermaster.cluster_invites (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    invited_tenant_id UUID NOT NULL REFERENCES quartermaster.tenants(id) ON DELETE CASCADE,

    -- ===== INVITE CONFIGURATION =====
    invite_token VARCHAR(128) UNIQUE NOT NULL DEFAULT quartermaster.generate_random_string(32),
    access_level VARCHAR(50) DEFAULT 'subscriber',
    resource_limits JSONB DEFAULT '{}',

    -- ===== STATUS =====
    status VARCHAR(20) DEFAULT 'pending',

    -- ===== LIFECYCLE =====
    created_by UUID NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP,
    accepted_at TIMESTAMP,

    CONSTRAINT chk_invite_status CHECK (status IN ('pending', 'accepted', 'expired', 'revoked')),
    UNIQUE(cluster_id, invited_tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_qm_cluster_invites_cluster ON quartermaster.cluster_invites(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_invites_tenant ON quartermaster.cluster_invites(invited_tenant_id);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_invites_token ON quartermaster.cluster_invites(invite_token);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_invites_pending ON quartermaster.cluster_invites(status) WHERE status = 'pending';

-- ============================================================================
-- HOT-PATH INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_qm_services_type ON quartermaster.services(type);
CREATE INDEX IF NOT EXISTS idx_qm_service_instances_service_status_created ON quartermaster.service_instances(service_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_qm_service_instances_status_last_check ON quartermaster.service_instances(status, last_health_check);
CREATE UNIQUE INDEX IF NOT EXISTS idx_qm_infrastructure_nodes_wireguard_ip_unique
    ON quartermaster.infrastructure_nodes((wireguard_ip::text))
    WHERE wireguard_ip IS NOT NULL;

-- ============================================================================
-- SERVICE EVENT OUTBOX
-- ============================================================================
-- Durable outbox for Quartermaster-emitted service events (TenantEvent /
-- ClusterEvent / etc.) to Decklog. Drain worker dispatches pending rows
-- with exponential backoff. Payload is the full pb.ServiceEvent in
-- protojson — the typed oneof variants ride inside it.

CREATE TABLE IF NOT EXISTS quartermaster.service_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    TEXT NOT NULL,
    tenant_id     UUID NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_qm_service_event_outbox_pending
    ON quartermaster.service_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_qm_service_event_outbox_tenant
    ON quartermaster.service_event_outbox(tenant_id, created_at DESC);

-- ============================================================================
-- NAVIGATOR CUSTOM-DOMAIN OUTBOX
-- ============================================================================
-- Durable outbox for the BYO custom-domain hand-off to Navigator. UpdateTenant
-- inserts a row in the same tx as the tenants UPDATE so a Navigator outage
-- can't leave QM saying the tenant has a custom_domain while Navigator never
-- created the verification + cert lifecycle row. Drain worker calls
-- Navigator.EnsureCustomDomain / RemoveCustomDomain with exponential backoff.

CREATE TABLE IF NOT EXISTS quartermaster.navigator_custom_domain_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    domain    TEXT NOT NULL,
    action    TEXT NOT NULL CHECK (action IN ('ensure', 'remove')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_qm_navigator_custom_domain_outbox_pending
    ON quartermaster.navigator_custom_domain_outbox(created_at)
    WHERE completed_at IS NULL;

-- ============================================================================
-- NAVIGATOR TENANT-ALIAS OUTBOX
-- ============================================================================
-- Durable outbox for the platform subdomain-alias hand-off to Navigator.
-- Tenant create/rename, billing tier changes, and cluster-access changes all
-- enqueue rows in the same tx as the mutation, so a Navigator outage cannot
-- lose the intent. Rows are self-contained: the paid/active decision is made
-- at enqueue time, so the drain worker dispatches purely from stored fields.
--
-- seq (BIGSERIAL) is the monotonic enqueue order. Rows enqueued in the same
-- tx (retire(old) + ensure(new)) share an identical created_at, so the worker
-- serializes per tenant by seq, NOT created_at. The claim query dispatches at
-- most one in-flight row per tenant (no lower-seq incomplete row) so a newer
-- remove can never overtake an older ensure across replicas.
CREATE TABLE IF NOT EXISTS quartermaster.navigator_tenant_alias_outbox (
    id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq BIGSERIAL NOT NULL,
    tenant_id  UUID NOT NULL,
    subdomain  TEXT,
    cluster_id TEXT,
    reason     TEXT,
    action     TEXT NOT NULL CHECK (action IN ('ensure', 'retire', 'remove', 'remove_cluster')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ,
    -- ensure/retire need a non-blank label; remove_cluster needs a non-blank
    -- cluster; remove is tenant-only (subdomain optional, for audit).
    CONSTRAINT chk_alias_outbox_subdomain CHECK (action NOT IN ('ensure', 'retire') OR NULLIF(btrim(subdomain), '') IS NOT NULL),
    CONSTRAINT chk_alias_outbox_cluster   CHECK (action <> 'remove_cluster' OR NULLIF(btrim(cluster_id), '') IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_qm_navigator_tenant_alias_outbox_pending
    ON quartermaster.navigator_tenant_alias_outbox(tenant_id, seq)
    WHERE completed_at IS NULL;

-- Schema baseline identity marker. Records that this database was created from the
-- consolidated baseline at this floor, so the migration min-version guard treats
-- below-floor migrations as folded into the baseline (not missing). An existing
-- cluster upgraded in place has no marker and is checked for ledger completeness
-- instead. The floor value is kept in sync with provisioner.schemaMigrationBaselineFloor
-- by TestBaselineMarkerFloorMatchesConst. See docs/standards/schema-migrations.md.
CREATE TABLE IF NOT EXISTS public._schema_baseline (
    floor TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
    SELECT 'v0.2.96' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
