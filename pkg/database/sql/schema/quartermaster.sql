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

-- Stream status enumerations (legacy - may be moved to Commodore)
DO $$ BEGIN
    CREATE TYPE stream_status AS ENUM ('offline','live','terminated');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE mist_stream_status AS ENUM ('offline','init','boot','wait','ready','shutdown','invalid');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

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
    
    -- ===== INFRASTRUCTURE CONNECTIVITY =====
    kafka_topic_prefix VARCHAR(100),
    kafka_brokers TEXT[],
    database_url TEXT,
    
    -- ===== STATUS & LIFECYCLE =====
    is_provider BOOLEAN DEFAULT FALSE,
    is_active BOOLEAN DEFAULT TRUE,
    trial_ends_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
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
    
    -- ===== CURRENT USAGE =====
    current_stream_count INTEGER DEFAULT 0,
    current_viewer_count INTEGER DEFAULT 0,
    current_bandwidth_mbps INTEGER DEFAULT 0,
    
    -- ===== STATUS & HEALTH =====
    is_active BOOLEAN DEFAULT TRUE,
    is_default_cluster BOOLEAN DEFAULT FALSE,
    health_status VARCHAR(50) DEFAULT 'healthy',
    last_seen TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
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
    
    -- ===== NETWORKING =====
    internal_ip INET,
    external_ip INET,
    wireguard_ip INET,
    wireguard_public_key TEXT,
    
    -- ===== GEOGRAPHIC LOCATION =====
    region VARCHAR(50),
    availability_zone VARCHAR(50),
    latitude DECIMAL(10, 8),
    longitude DECIMAL(11, 8),
    
    -- ===== HARDWARE RESOURCES =====
    cpu_cores INTEGER,
    memory_gb INTEGER,
    disk_gb INTEGER,
    
    -- ===== STATUS & HEALTH =====
    status VARCHAR(50) DEFAULT 'active',
    last_heartbeat TIMESTAMP,
    
    -- ===== METADATA & CONFIGURATION =====
    tags JSONB DEFAULT '{}',
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
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
    
    -- ===== RESOURCE USAGE =====
    cpu_usage_percent DECIMAL(5,2),
    memory_usage_mb INTEGER,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- CORE INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_qm_infrastructure_clusters_cluster_type ON quartermaster.infrastructure_clusters(cluster_type);
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

    -- ===== STATUS =====
    is_active BOOLEAN DEFAULT true,
    granted_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(tenant_id, cluster_id)
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

-- Generate random alphanumeric strings for keys and tokens
CREATE OR REPLACE FUNCTION quartermaster.generate_random_string(length INTEGER) RETURNS TEXT AS $$
DECLARE
    chars TEXT := 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    result TEXT := '';
    i INTEGER := 0;
BEGIN
    FOR i IN 1..length LOOP
        result := result || substr(chars, floor(random() * length(chars) + 1)::INTEGER, 1);
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
    token VARCHAR(128) UNIQUE NOT NULL,
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

CREATE INDEX IF NOT EXISTS idx_qm_bootstrap_tokens_token ON quartermaster.bootstrap_tokens(token);
CREATE INDEX IF NOT EXISTS idx_qm_bootstrap_tokens_kind ON quartermaster.bootstrap_tokens(kind);