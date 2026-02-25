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
    deployment_tier VARCHAR(50) DEFAULT 'global',
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
    
    -- ===== CAPACITY LIMITS =====
    max_concurrent_streams INTEGER DEFAULT 1000,
    max_concurrent_viewers INTEGER DEFAULT 100000,
    max_bandwidth_mbps INTEGER DEFAULT 10000,

    -- ===== STATUS & HEALTH =====
    is_active BOOLEAN DEFAULT TRUE,
    is_default_cluster BOOLEAN DEFAULT FALSE,
    is_platform_official BOOLEAN DEFAULT FALSE,
    health_status VARCHAR(50) DEFAULT 'healthy',
    last_seen TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    -- ===== S3 STORAGE CONFIGURATION =====
    s3_bucket VARCHAR(255),
    s3_endpoint VARCHAR(500),
    s3_region VARCHAR(50),

    -- ===== MARKETPLACE CONFIGURATION =====
    visibility VARCHAR(20) DEFAULT 'private',
    pricing_model VARCHAR(20) DEFAULT 'free_unmetered',
    monthly_price_cents INTEGER DEFAULT 0,
    metered_rate_config JSONB DEFAULT '{}',
    requires_approval BOOLEAN DEFAULT FALSE,
    short_description VARCHAR(500),

    -- ===== CONSTRAINTS =====
    CONSTRAINT chk_cluster_visibility CHECK (visibility IN ('public', 'unlisted', 'private')),
    CONSTRAINT chk_cluster_pricing_model CHECK (pricing_model IN ('free_unmetered', 'metered', 'monthly', 'custom', 'tier_inherit'))
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

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT fk_qm_service_instances_node_cluster
        FOREIGN KEY (node_id, cluster_id)
        REFERENCES quartermaster.infrastructure_nodes(node_id, cluster_id)
);

-- ============================================================================
-- FOGHORN-CLUSTER ASSIGNMENTS (many-to-many)
-- ============================================================================

-- Maps Foghorn instances to clusters they serve. A single Foghorn can serve
-- multiple clusters (shared Foghorn), and a cluster can have multiple Foghorns (HA).
-- Replaces the 1:1 service_instances.cluster_id binding for Foghorn routing.
CREATE TABLE IF NOT EXISTS quartermaster.foghorn_cluster_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    foghorn_instance_id UUID NOT NULL REFERENCES quartermaster.service_instances(id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES quartermaster.infrastructure_clusters(cluster_id) ON DELETE CASCADE,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(foghorn_instance_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_qm_fca_cluster ON quartermaster.foghorn_cluster_assignments(cluster_id) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_qm_fca_foghorn ON quartermaster.foghorn_cluster_assignments(foghorn_instance_id);

-- ============================================================================
-- CORE INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_clusters_cluster_type ON quartermaster.infrastructure_clusters(cluster_type);
CREATE INDEX IF NOT EXISTS idx_qm_clusters_platform_official ON quartermaster.infrastructure_clusters(is_platform_official) WHERE is_platform_official = true;
CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_clusters_owner_tenant ON quartermaster.infrastructure_clusters(owner_tenant_id);
CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_nodes_cluster_id ON quartermaster.infrastructure_nodes(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_services_plane ON quartermaster.services(plane);
CREATE INDEX IF NOT EXISTS idx_qm_cluster_services_cluster_id ON quartermaster.cluster_services(cluster_id);
CREATE INDEX IF NOT EXISTS idx_qm_service_instances_cluster_id ON quartermaster.service_instances(cluster_id);

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

    -- ===== RESOURCE LIMITS (synced from Purser custom_allocations) =====
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
    ON quartermaster.infrastructure_nodes(wireguard_ip)
    WHERE wireguard_ip IS NOT NULL;
