-- FrameWorks Multi-Tenant Database Schema
-- Compatible with both PostgreSQL and YugabyteDB
-- Designed for full enterprise SaaS implementation

-- Create database extensions if they don't exist
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- CORE TENANT MANAGEMENT
-- ============================================================================

-- Tenants table - the root of multi-tenancy
CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    subdomain VARCHAR(100) UNIQUE,
    custom_domain VARCHAR(255),
    
    -- Subscription & billing
    plan VARCHAR(50) DEFAULT 'free',
    max_streams INTEGER DEFAULT 3,
    max_storage_gb INTEGER DEFAULT 10,
    max_bandwidth_gb INTEGER DEFAULT 100,
    max_users INTEGER DEFAULT 1,
    
    -- Features
    is_recording_enabled BOOLEAN DEFAULT FALSE,
    is_analytics_enabled BOOLEAN DEFAULT TRUE,
    is_api_enabled BOOLEAN DEFAULT FALSE,
    is_white_label_enabled BOOLEAN DEFAULT FALSE,
    
    -- Branding
    logo_url VARCHAR(500),
    primary_color VARCHAR(7) DEFAULT '#6366f1',
    secondary_color VARCHAR(7) DEFAULT '#f59e0b',
    
    -- DEPLOYMENT ROUTING (supports multiple tiers)
    deployment_tier VARCHAR(50) DEFAULT 'global', -- Simple deployment tier (backward compatibility)
    deployment_model VARCHAR(50) DEFAULT 'shared', -- Deployment model (shared, dedicated, hybrid)
    primary_deployment_tier VARCHAR(50) DEFAULT 'global', -- Their main/preferred tier
    allowed_deployment_tiers TEXT[] DEFAULT ARRAY['global'], -- All tiers they can use
    primary_cluster_id VARCHAR(100), -- Simple cluster assignment
    kafka_topic_prefix VARCHAR(100), -- Base prefix for this tenant
    kafka_brokers TEXT[], -- Custom Kafka brokers for this tenant
    database_url TEXT, -- Custom database URL for this tenant
    
    -- Billing
    plan_id UUID,
    billing_status VARCHAR(50) NOT NULL DEFAULT 'active',
    payment_method VARCHAR(50),
    
    -- Status
    is_active BOOLEAN DEFAULT TRUE,
    trial_ends_at TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- CORE INFRASTRUCTURE
-- ============================================================================

-- Infrastructure clusters (just the basics for routing)
CREATE TABLE IF NOT EXISTS infrastructure_clusters (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_name VARCHAR(255) NOT NULL,
    cluster_type VARCHAR(50) NOT NULL, -- central, regional, edge
    
    -- Ownership and tenancy
    owner_tenant_id UUID REFERENCES tenants(id), -- NULL = shared cluster, set = dedicated
    deployment_model VARCHAR(50) DEFAULT 'shared', -- shared, dedicated, hybrid
    
    -- Basic routing info
    base_url VARCHAR(500) NOT NULL,
    database_url TEXT, -- NULL = use shared DB, set = dedicated DB
    periscope_url TEXT, -- NULL = use shared analytics, set = dedicated analytics
    
    -- Infrastructure endpoints
    kafka_brokers TEXT[],
    
    -- Capacity limits and current usage
    max_concurrent_streams INTEGER DEFAULT 1000,
    max_concurrent_viewers INTEGER DEFAULT 100000,
    max_bandwidth_mbps INTEGER DEFAULT 10000,
    current_stream_count INTEGER DEFAULT 0,
    current_viewer_count INTEGER DEFAULT 0,
    current_bandwidth_mbps INTEGER DEFAULT 0,
    
    -- Health
    is_active BOOLEAN DEFAULT TRUE,
    health_status VARCHAR(50) DEFAULT 'healthy', -- healthy, degraded, unhealthy, full
    last_seen TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Infrastructure nodes (physical/virtual machines in clusters)
CREATE TABLE IF NOT EXISTS infrastructure_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    
    -- Node details
    node_name VARCHAR(255) NOT NULL,
    node_type VARCHAR(50) NOT NULL, -- compute, media, storage, control
    
    -- Network configuration
    internal_ip INET,
    external_ip INET,
    wireguard_ip INET,
    wireguard_public_key TEXT,
    
    -- Geographic location
    region VARCHAR(50),
    availability_zone VARCHAR(50),
    latitude DECIMAL(10, 8),
    longitude DECIMAL(11, 8),
    
    -- Resource specs
    cpu_cores INTEGER,
    memory_gb INTEGER,
    disk_gb INTEGER,
    
    -- Status and health
    status VARCHAR(50) DEFAULT 'active', -- active, maintenance, decommissioned
    health_score DECIMAL(3,2) DEFAULT 1.0,
    last_heartbeat TIMESTAMP,
    
    -- Metadata
    tags JSONB DEFAULT '{}',
    metadata JSONB DEFAULT '{}',
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- SERVICE CATALOG & ORCHESTRATION
-- ============================================================================

-- Service catalog (defines all available services)
CREATE TABLE IF NOT EXISTS services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id VARCHAR(100) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    plane VARCHAR(50) NOT NULL, -- control, data, media
    description TEXT,
    
    -- Service configuration
    default_port INTEGER,
    health_check_path VARCHAR(255),
    docker_image VARCHAR(255),
    
    -- Service metadata
    version VARCHAR(50),
    dependencies TEXT[], -- Array of service_ids this service depends on
    tags JSONB DEFAULT '{}',
    
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Service assignments to clusters (replaces ad-hoc roles JSON)
CREATE TABLE IF NOT EXISTS cluster_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    service_id VARCHAR(100) NOT NULL REFERENCES services(service_id),
    
    -- Deployment configuration
    desired_state VARCHAR(50) DEFAULT 'running', -- running, stopped, maintenance
    desired_replicas INTEGER DEFAULT 1,
    current_replicas INTEGER DEFAULT 0,
    
    -- Service-specific configuration
    config_blob JSONB DEFAULT '{}',
    environment_vars JSONB DEFAULT '{}',
    
    -- Resource limits
    cpu_limit DECIMAL(4,2),
    memory_limit_mb INTEGER,
    
    -- Health and status
    health_status VARCHAR(50) DEFAULT 'unknown', -- healthy, degraded, unhealthy, unknown
    last_deployed TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(cluster_id, service_id)
);

-- Service instances (tracks individual running instances)
CREATE TABLE IF NOT EXISTS service_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id VARCHAR(100) UNIQUE NOT NULL,
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    node_id VARCHAR(100) REFERENCES infrastructure_nodes(node_id),
    service_id VARCHAR(100) NOT NULL REFERENCES services(service_id),
    
    -- Instance details
    version VARCHAR(50),
    port INTEGER,
    process_id INTEGER,
    container_id VARCHAR(255),
    
    -- Status tracking
    status VARCHAR(50) DEFAULT 'starting', -- starting, running, stopping, stopped, failed
    health_status VARCHAR(50) DEFAULT 'unknown',
    
    -- Timestamps
    started_at TIMESTAMP,
    stopped_at TIMESTAMP,
    last_health_check TIMESTAMP,
    
    -- Resource usage
    cpu_usage_percent DECIMAL(5,2),
    memory_usage_mb INTEGER,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- ANSIBLE DYNAMIC INVENTORY
-- ============================================================================

-- Ansible groups (dynamic grouping criteria)
CREATE TABLE IF NOT EXISTS ansible_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_name VARCHAR(100) UNIQUE NOT NULL,
    description TEXT,
    
    -- Dynamic criteria (JSON query for node selection)
    criteria JSONB NOT NULL DEFAULT '{}',
    
    -- Group variables
    group_vars JSONB DEFAULT '{}',
    
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Node to Ansible group mapping (computed from criteria)
CREATE TABLE IF NOT EXISTS node_ansible_group_map (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id VARCHAR(100) NOT NULL REFERENCES infrastructure_nodes(node_id),
    group_name VARCHAR(100) NOT NULL REFERENCES ansible_groups(group_name),
    
    -- Computed at runtime by matching criteria
    computed_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(node_id, group_name)
);

-- ============================================================================
-- BILLING PLANS
-- ============================================================================

-- Billing plans
CREATE TABLE IF NOT EXISTS billing_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL UNIQUE,
    price DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    billing_period VARCHAR(20) NOT NULL DEFAULT 'monthly',
    features JSONB NOT NULL DEFAULT '{}',
    is_active BOOLEAN NOT NULL DEFAULT true,
    
    -- Metering configuration
    is_metered BOOLEAN NOT NULL DEFAULT false,
    metered_clusters TEXT[] DEFAULT NULL, -- NULL = all clusters metered, empty array = no clusters metered
    metering_rates JSONB NOT NULL DEFAULT '{
        "stream_hours": 0.00,
        "egress_gb": 0.00,
        "recording_gb": 0.00,
        "peak_bandwidth_mbps": 0.00
    }',
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Insert default plans
INSERT INTO billing_plans (name, price, currency, billing_period, features, is_active, is_metered, metered_clusters, metering_rates) VALUES
('free', 0.00, 'EUR', 'monthly', '{
    "concurrent_streams": 10,
    "bandwidth_mbps": 100,
    "max_concurrent_viewers": 100
}', true, false, '{}', '{
    "stream_hours": 0.00,
    "egress_gb": 0.00,
    "recording_gb": 0.00,
    "peak_bandwidth_mbps": 0.00
}'),

('supporter', 50.00, 'EUR', 'monthly', '{
    "concurrent_streams": 100,
    "bandwidth_mbps": 250,
    "max_concurrent_viewers": 300
}', true, true, ARRAY['global-primary'], '{
    "stream_hours": 0.10,
    "egress_gb": 0.05,
    "recording_gb": 0.02,
    "peak_bandwidth_mbps": 0.01
}'),

('developer', 250.00, 'EUR', 'monthly', '{
    "concurrent_streams": 1000,
    "bandwidth_mbps": 1000,
    "max_concurrent_viewers": 1000
}', true, true, NULL, '{
    "stream_hours": 0.08,
    "egress_gb": 0.04,
    "recording_gb": 0.015,
    "peak_bandwidth_mbps": 0.008
}'),

('production', 1000.00, 'EUR', 'monthly', '{
    "concurrent_streams": -1,
    "bandwidth_mbps": 5000,
    "max_concurrent_viewers": 5000
}', true, true, NULL, '{
    "stream_hours": 0.06,
    "egress_gb": 0.03,
    "recording_gb": 0.01,
    "peak_bandwidth_mbps": 0.005
}'),

('enterprise', 0.00, 'EUR', 'monthly', '{
    "concurrent_streams": -1,
    "bandwidth_mbps": -1,
    "max_concurrent_viewers": -1
}', true, true, NULL, '{
    "stream_hours": 0.04,
    "egress_gb": 0.02,
    "recording_gb": 0.008,
    "peak_bandwidth_mbps": 0.004
}')

ON CONFLICT (name) DO UPDATE SET
    price = EXCLUDED.price,
    features = EXCLUDED.features,
    is_metered = EXCLUDED.is_metered,
    metered_clusters = EXCLUDED.metered_clusters,
    metering_rates = EXCLUDED.metering_rates;

-- ============================================================================
-- CORE TENANT MANAGEMENT
-- ============================================================================

-- Multi-tier tenant-to-cluster mapping with capacity limits
CREATE TABLE IF NOT EXISTS tenant_cluster_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    
    -- Assignment configuration
    deployment_tier VARCHAR(50), -- Which tier this assignment represents (optional)
    priority INTEGER DEFAULT 1, -- Lower = higher priority (1 = primary, 2 = fallback, etc.)
    is_primary BOOLEAN DEFAULT FALSE, -- Is this the primary cluster for the tenant
    is_active BOOLEAN DEFAULT TRUE,
    
    -- Per-tenant capacity limits on this cluster (NULL = no limit)
    max_streams_on_cluster INTEGER,
    max_viewers_on_cluster INTEGER,
    max_bandwidth_mbps_on_cluster INTEGER,
    fallback_when_full BOOLEAN DEFAULT FALSE, -- Can fallback to lower priority
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(tenant_id, cluster_id)
);

-- ============================================================================
-- USER MANAGEMENT (TENANT-SCOPED)
-- ============================================================================

-- Users table with tenant isolation
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    
    -- Profile
    first_name VARCHAR(100),
    last_name VARCHAR(100),
    avatar_url VARCHAR(500),
    
    -- Role within tenant
    role VARCHAR(50) DEFAULT 'member', -- owner, admin, member, viewer, service
    permissions TEXT[] DEFAULT ARRAY['streams:read'],
    
    -- Status
    is_active BOOLEAN DEFAULT TRUE,
    verified BOOLEAN DEFAULT FALSE,
    verification_token VARCHAR(255),
    token_expires_at TIMESTAMP,
    last_login_at TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    -- Unique email per tenant
    UNIQUE(tenant_id, email)
);

-- Sessions table (tenant-scoped)
CREATE TABLE IF NOT EXISTS sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    session_token VARCHAR(255) UNIQUE NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    
    -- Session metadata
    ip_address INET,
    user_agent TEXT,
    
    created_at TIMESTAMP DEFAULT NOW()
);

-- API tokens table for developer access (tenant-scoped)
CREATE TABLE IF NOT EXISTS api_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    token_value VARCHAR(50) UNIQUE NOT NULL,
    token_name VARCHAR(255) NOT NULL,
    permissions TEXT[] DEFAULT ARRAY['read'],
    
    -- Usage tracking
    usage_count INTEGER DEFAULT 0,
    last_used_at TIMESTAMP,
    last_used_ip INET,
    
    is_active BOOLEAN DEFAULT TRUE,
    expires_at TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- STREAM MANAGEMENT (TENANT-SCOPED)
-- ============================================================================

-- Streams table with tenant isolation (CONTROL PLANE ONLY)
CREATE TABLE IF NOT EXISTS streams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    -- Security: Separate identifiers for different purposes
    stream_key VARCHAR(255) UNIQUE NOT NULL,      -- For RTMP ingest (secret)
    playback_id VARCHAR(255) UNIQUE NOT NULL,     -- For public playback URLs
    internal_name VARCHAR(255) UNIQUE NOT NULL,   -- For MistServer internal routing
    
    -- Stream metadata
    title VARCHAR(255) NOT NULL,
    description TEXT,
    thumbnail_url VARCHAR(500),
    
    -- Stream configuration (Control Plane settings only)
    is_recording_enabled BOOLEAN DEFAULT FALSE,
    is_public BOOLEAN DEFAULT TRUE,
    max_viewers INTEGER,
    password VARCHAR(255), -- Stream password protection
    
    -- Operational state (Control Plane lifecycle tracking)
    status VARCHAR(20) DEFAULT 'offline', -- offline, live, terminated
    start_time TIMESTAMP,
    end_time TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Stream keys table - for managing multiple keys per stream (tenant-scoped)
CREATE TABLE IF NOT EXISTS stream_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    
    key_value VARCHAR(255) UNIQUE NOT NULL,
    key_name VARCHAR(100),
    
    is_active BOOLEAN DEFAULT TRUE,
    last_used_at TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- RECORDINGS & CLIPS (TENANT-SCOPED)
-- ============================================================================

-- Recordings table for VOD content
CREATE TABLE IF NOT EXISTS recordings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    -- Recording metadata
    title VARCHAR(255),
    duration INTEGER, -- seconds
    file_size_bytes BIGINT,
    file_path VARCHAR(500),
    thumbnail_url VARCHAR(500),
    
    -- Processing status
    status VARCHAR(50) DEFAULT 'processing', -- processing, ready, failed
    transcoding_progress INTEGER DEFAULT 0,
    
    -- Access control
    is_public BOOLEAN DEFAULT FALSE,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Clips table (for stream clips) - tenant-scoped
CREATE TABLE IF NOT EXISTS clips (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    title VARCHAR(255),
    start_time BIGINT NOT NULL, -- milliseconds from stream start
    duration BIGINT NOT NULL,   -- milliseconds
    
    clip_url VARCHAR(500),
    thumbnail_url VARCHAR(500),
    
    status VARCHAR(50) DEFAULT 'processing',
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- ANALYTICS TABLES (TENANT-SCOPED)
-- ============================================================================

-- Stream analytics aggregated data (DATA PLANE)
CREATE TABLE IF NOT EXISTS stream_analytics (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    
    internal_name VARCHAR(255) NOT NULL,
    
    -- Session timing (for calculating total view time)
    session_start_time TIMESTAMP,
    session_end_time TIMESTAMP,
    total_session_duration INTEGER DEFAULT 0, -- seconds
    
    -- Real-time metrics
    current_viewers INTEGER DEFAULT 0,
    peak_viewers INTEGER DEFAULT 0,
    total_connections INTEGER DEFAULT 0,
    
    -- Technical metrics
    bandwidth_in BIGINT DEFAULT 0,
    bandwidth_out BIGINT DEFAULT 0,
    total_bandwidth_gb DECIMAL(15,6) DEFAULT 0,
    bitrate_kbps INTEGER,
    resolution VARCHAR(50),
    
    -- Performance metrics
    packets_sent BIGINT DEFAULT 0,
    packets_lost BIGINT DEFAULT 0,
    packets_retrans BIGINT DEFAULT 0,
    upbytes BIGINT DEFAULT 0,
    downbytes BIGINT DEFAULT 0,
    
    -- Stream health
    first_ms BIGINT,
    last_ms BIGINT,
    track_count INTEGER DEFAULT 0,
    inputs INTEGER DEFAULT 0,
    outputs INTEGER DEFAULT 0,
    
    -- Track details
    track_details JSONB,
    health_data JSONB,
    
    -- Geographic data
    node_id VARCHAR(100),
    node_name VARCHAR(255),
    latitude DECIMAL(10, 8),
    longitude DECIMAL(11, 8),
    location VARCHAR(255),
    
    -- Stream status (Data Plane perspective)
    status VARCHAR(50) DEFAULT 'offline',
    last_updated TIMESTAMP DEFAULT NOW(),
    created_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(tenant_id, internal_name)
);

-- ============================================================================
-- INDEXES FOR PERFORMANCE
-- ============================================================================

-- Infrastructure cluster indexes
CREATE INDEX IF NOT EXISTS idx_infrastructure_clusters_cluster_type ON infrastructure_clusters(cluster_type);
CREATE INDEX IF NOT EXISTS idx_infrastructure_clusters_owner_tenant ON infrastructure_clusters(owner_tenant_id);
CREATE INDEX IF NOT EXISTS idx_infrastructure_clusters_health ON infrastructure_clusters(health_status);
CREATE INDEX IF NOT EXISTS idx_infrastructure_clusters_active ON infrastructure_clusters(is_active);

-- Infrastructure node indexes
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_cluster_id ON infrastructure_nodes(cluster_id);
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_node_type ON infrastructure_nodes(node_type);
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_status ON infrastructure_nodes(status);
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_region ON infrastructure_nodes(region);
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_health ON infrastructure_nodes(health_score);
CREATE INDEX IF NOT EXISTS idx_infrastructure_nodes_heartbeat ON infrastructure_nodes(last_heartbeat);

-- Service catalog indexes
CREATE INDEX IF NOT EXISTS idx_services_plane ON services(plane);
CREATE INDEX IF NOT EXISTS idx_services_active ON services(is_active);

-- Cluster services indexes
CREATE INDEX IF NOT EXISTS idx_cluster_services_cluster_id ON cluster_services(cluster_id);
CREATE INDEX IF NOT EXISTS idx_cluster_services_service_id ON cluster_services(service_id);
CREATE INDEX IF NOT EXISTS idx_cluster_services_desired_state ON cluster_services(desired_state);
CREATE INDEX IF NOT EXISTS idx_cluster_services_health ON cluster_services(health_status);

-- Service instances indexes
CREATE INDEX IF NOT EXISTS idx_service_instances_cluster_id ON service_instances(cluster_id);
CREATE INDEX IF NOT EXISTS idx_service_instances_node_id ON service_instances(node_id);
CREATE INDEX IF NOT EXISTS idx_service_instances_service_id ON service_instances(service_id);
CREATE INDEX IF NOT EXISTS idx_service_instances_status ON service_instances(status);
CREATE INDEX IF NOT EXISTS idx_service_instances_health ON service_instances(health_status);

-- Ansible group indexes
CREATE INDEX IF NOT EXISTS idx_ansible_groups_active ON ansible_groups(is_active);

-- Node group mapping indexes
CREATE INDEX IF NOT EXISTS idx_node_ansible_group_map_node ON node_ansible_group_map(node_id);
CREATE INDEX IF NOT EXISTS idx_node_ansible_group_map_group ON node_ansible_group_map(group_name);

-- Tenant indexes
CREATE INDEX IF NOT EXISTS idx_tenants_subdomain ON tenants(subdomain);
CREATE INDEX IF NOT EXISTS idx_tenants_custom_domain ON tenants(custom_domain);
CREATE INDEX IF NOT EXISTS idx_tenants_active ON tenants(is_active);

-- User indexes
CREATE INDEX IF NOT EXISTS idx_users_tenant_id ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_users_tenant_email ON users(tenant_id, email);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(tenant_id, role);

-- Session indexes
CREATE INDEX IF NOT EXISTS idx_sessions_tenant_id ON sessions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(session_token);

-- API token indexes
CREATE INDEX IF NOT EXISTS idx_api_tokens_tenant_id ON api_tokens(tenant_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token_value ON api_tokens(token_value);
CREATE INDEX IF NOT EXISTS idx_api_tokens_active ON api_tokens(tenant_id, is_active, expires_at);

-- Stream indexes
CREATE INDEX IF NOT EXISTS idx_streams_tenant_id ON streams(tenant_id);
CREATE INDEX IF NOT EXISTS idx_streams_user_id ON streams(user_id);
CREATE INDEX IF NOT EXISTS idx_streams_tenant_user ON streams(tenant_id, user_id);
CREATE INDEX IF NOT EXISTS idx_streams_stream_key ON streams(stream_key);
CREATE INDEX IF NOT EXISTS idx_streams_playback_id ON streams(playback_id);
CREATE INDEX IF NOT EXISTS idx_streams_internal_name ON streams(internal_name);
CREATE INDEX IF NOT EXISTS idx_streams_status ON streams(tenant_id, status);

-- Stream keys indexes
CREATE INDEX IF NOT EXISTS idx_stream_keys_tenant_id ON stream_keys(tenant_id);
CREATE INDEX IF NOT EXISTS idx_stream_keys_stream_id ON stream_keys(stream_id);
CREATE INDEX IF NOT EXISTS idx_stream_keys_key_value ON stream_keys(key_value);
CREATE INDEX IF NOT EXISTS idx_stream_keys_active ON stream_keys(tenant_id, is_active);

-- Recording indexes
CREATE INDEX IF NOT EXISTS idx_recordings_tenant_id ON recordings(tenant_id);
CREATE INDEX IF NOT EXISTS idx_recordings_stream_id ON recordings(stream_id);
CREATE INDEX IF NOT EXISTS idx_recordings_user_id ON recordings(user_id);
CREATE INDEX IF NOT EXISTS idx_recordings_status ON recordings(tenant_id, status);

-- Clip indexes
CREATE INDEX IF NOT EXISTS idx_clips_tenant_id ON clips(tenant_id);
CREATE INDEX IF NOT EXISTS idx_clips_stream_id ON clips(stream_id);
CREATE INDEX IF NOT EXISTS idx_clips_user_id ON clips(user_id);
CREATE INDEX IF NOT EXISTS idx_clips_status ON clips(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_clips_created_at ON clips(tenant_id, created_at DESC);

-- Analytics indexes
CREATE INDEX IF NOT EXISTS idx_stream_analytics_tenant_id ON stream_analytics(tenant_id);
CREATE INDEX IF NOT EXISTS idx_stream_analytics_internal_name ON stream_analytics(internal_name);
CREATE INDEX IF NOT EXISTS idx_stream_analytics_tenant_internal ON stream_analytics(tenant_id, internal_name);
CREATE INDEX IF NOT EXISTS idx_stream_analytics_status ON stream_analytics(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_stream_analytics_last_updated ON stream_analytics(tenant_id, last_updated);

-- ============================================================================
-- FUNCTIONS FOR TENANT-AWARE OPERATIONS
-- ============================================================================

-- Function to generate secure random strings
CREATE OR REPLACE FUNCTION generate_random_string(length INTEGER) RETURNS TEXT AS $$
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

-- Function to create a new stream with all required identifiers (tenant-aware)
CREATE OR REPLACE FUNCTION create_user_stream(p_tenant_id UUID, p_user_id UUID, p_title VARCHAR DEFAULT 'My Stream') 
RETURNS TABLE(stream_id UUID, stream_key VARCHAR, playback_id VARCHAR, internal_name VARCHAR) AS $$
DECLARE
    new_stream_id UUID;
    new_stream_key VARCHAR(32);
    new_playback_id VARCHAR(16);
    new_internal_name VARCHAR(64);
BEGIN
    -- Generate unique identifiers
    new_stream_id := gen_random_uuid();
    new_stream_key := 'sk_' || generate_random_string(28);
    new_playback_id := generate_random_string(16);
    new_internal_name := new_stream_id::TEXT;
    
    -- Insert the stream
    INSERT INTO streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
    VALUES (new_stream_id, p_tenant_id, p_user_id, new_stream_key, new_playback_id, new_internal_name, p_title);
    
    -- Also create an entry in stream_keys for backward compatibility
    INSERT INTO stream_keys (tenant_id, stream_id, key_value, key_name, is_active)
    VALUES (p_tenant_id, new_stream_id, new_stream_key, 'Primary Key', TRUE);
    
    RETURN QUERY SELECT new_stream_id, new_stream_key, new_playback_id, new_internal_name;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- DEMO DATA
-- ============================================================================

-- Insert default global cluster
INSERT INTO infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps) VALUES
('central-primary', 'Central Primary Cluster', 'central', 'demo.frameworks.network', 10000, 1000000, 100000)
ON CONFLICT (cluster_id) DO NOTHING;

-- Insert service catalog (minimal; other services can be added via API)
INSERT INTO services (service_id, name, plane, description, default_port, health_check_path, docker_image) VALUES
('api_tenants', 'Quartermaster', 'control', 'Tenant and cluster management service', 9008, '/health', 'frameworks/quartermaster')
ON CONFLICT (service_id) DO NOTHING;
-- Assign services to central cluster (optional pre-seed)
INSERT INTO cluster_services (cluster_id, service_id, desired_state, desired_replicas, config_blob) VALUES
('central-primary', 'api_tenants', 'running', 1, '{"database_url": "postgres://frameworks_user:frameworks_dev@postgres:5432/frameworks"}')
ON CONFLICT (cluster_id, service_id) DO NOTHING;

-- Insert demo tenant (updated with new cluster reference)
INSERT INTO tenants (
    id, 
    name, 
    subdomain, 
    plan,
    max_streams,
    max_users,
    primary_cluster_id
) VALUES (
    '00000000-0000-0000-0000-000000000001',
    'Demo Organization',
    'demo',
    'pro',
    50,
    10,
    'central-primary'
) ON CONFLICT (id) DO NOTHING;

-- Insert demo user
INSERT INTO users (
    id, 
    tenant_id,
    email, 
    password_hash,
    first_name,
    last_name,
    role,
    permissions
) VALUES (
    '550e8400-e29b-41d4-a716-446655440000',
    '00000000-0000-0000-0000-000000000001',
    'demo@frameworks.dev',
    '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla',
    'Demo',
    'User',
    'owner',
    ARRAY['streams:read', 'streams:write', 'analytics:read', 'users:read', 'users:write', 'settings:write']
) ON CONFLICT (tenant_id, email) DO NOTHING;

-- Mark demo user as verified for development/testing
UPDATE users SET verified = TRUE WHERE email = 'demo@frameworks.dev' AND tenant_id = '00000000-0000-0000-0000-000000000001';

-- Insert service account user for service-to-service authentication
INSERT INTO users (
    id,
    tenant_id, 
    email,
    password_hash,
    first_name,
    last_name,
    role,
    permissions,
    is_active,
    verified
) VALUES (
    '00000000-0000-0000-0000-000000000000',
    '00000000-0000-0000-0000-000000000001',
    'service@internal',
    'no-login', -- Service accounts don't use password authentication
    'Service',
    'Account', 
    'service',
    ARRAY['*'], -- All permissions for service accounts
    TRUE,
    TRUE
) ON CONFLICT (tenant_id, email) DO NOTHING;



-- Create demo stream using the function
DO $$
DECLARE
    demo_tenant_id UUID := '00000000-0000-0000-0000-000000000001';
    demo_user_id UUID := '550e8400-e29b-41d4-a716-446655440000';
    stream_result RECORD;
BEGIN
    -- Check if demo stream already exists
    IF NOT EXISTS (SELECT 1 FROM streams WHERE tenant_id = demo_tenant_id AND user_id = demo_user_id) THEN
        SELECT * INTO stream_result FROM create_user_stream(demo_tenant_id, demo_user_id, 'Demo Stream');
    END IF;
END $$; 

-- ============================================================================
-- BILLING SYSTEM (PURSER DOMAIN)  
-- ============================================================================

-- Billing invoices
CREATE TABLE IF NOT EXISTS billing_invoices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_number VARCHAR(100) UNIQUE NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, paid, failed, cancelled
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    paid_amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    due_date TIMESTAMP WITH TIME ZONE NOT NULL,
    paid_at TIMESTAMP WITH TIME ZONE,
    base_amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    metered_amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    usage_details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Billing payments
CREATE TABLE IF NOT EXISTS billing_payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES billing_invoices(id) ON DELETE CASCADE,
    method VARCHAR(50) NOT NULL, -- mollie, crypto_btc, crypto_eth, crypto_usdc, crypto_lpt
    amount DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    tx_id VARCHAR(255), -- Transaction ID from payment provider or blockchain
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, confirmed, failed
    confirmed_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Crypto wallets for payments
CREATE TABLE IF NOT EXISTS crypto_wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id UUID NOT NULL REFERENCES billing_invoices(id) ON DELETE CASCADE,
    asset VARCHAR(10) NOT NULL, -- BTC, ETH, USDC, LPT
    wallet_address VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active', -- active, used, expired
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(invoice_id, asset)
);

-- ============================================================================
-- FLEXIBLE TIER-BASED BILLING SYSTEM
-- ============================================================================

-- Billing tiers with complex feature matrices and resource allocations
CREATE TABLE IF NOT EXISTS billing_tiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_name VARCHAR(100) NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT,
    
    -- Pricing structure
    base_price DECIMAL(10,2) NOT NULL DEFAULT 0.00,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    billing_period VARCHAR(20) NOT NULL DEFAULT 'monthly',
    
    -- Resource allocations per tier
    bandwidth_allocation JSONB DEFAULT '{}', -- {"min_mbps": 100, "max_mbps": 250, "burst_allowance": 500}
    storage_allocation JSONB DEFAULT '{}',   -- {"analytics_retention_days": 90, "recording_gb": 100}
    compute_allocation JSONB DEFAULT '{}',   -- {"gpu_access": false, "cpu_cores": 2, "memory_gb": 8}
    
    -- Feature matrix (what's unlocked at this tier)
    features JSONB NOT NULL DEFAULT '{}',    -- {"subdomain": true, "load_balancer": true, "calendar": true}
    
    -- Service levels
    support_level VARCHAR(50) DEFAULT 'community', -- community, basic, priority, enterprise
    sla_level VARCHAR(50) DEFAULT 'none',          -- none, standard, premium, custom
    
    -- Metering configuration (per-tier overages)
    metering_enabled BOOLEAN DEFAULT false,
    overage_rates JSONB DEFAULT '{}',        -- {"bandwidth_per_gb": 0.05, "storage_per_gb": 0.02}
    
    -- Tier metadata
    is_active BOOLEAN DEFAULT true,
    sort_order INTEGER DEFAULT 0,
    is_enterprise BOOLEAN DEFAULT false,     -- Special handling for enterprise tiers
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Cluster tier assignments (many-to-many: clusters can support multiple tiers)
CREATE TABLE IF NOT EXISTS cluster_tier_support (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    tier_id UUID NOT NULL REFERENCES billing_tiers(id),
    
    -- Cluster-specific tier configuration
    tier_config JSONB DEFAULT '{}',          -- Cluster-specific overrides for this tier
    capacity_allocation DECIMAL(5,2) DEFAULT 100.00, -- % of cluster capacity allocated to this tier
    priority_level INTEGER DEFAULT 0,        -- Traffic priority (higher = more priority)
    
    -- Availability
    is_available BOOLEAN DEFAULT true,
    effective_from TIMESTAMP DEFAULT NOW(),
    effective_until TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(cluster_id, tier_id)
);

-- Tenant billing subscriptions (replaces simple tenant_billing)
CREATE TABLE IF NOT EXISTS tenant_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL, -- References tenants(id) but no FK for microservice boundary
    tier_id UUID NOT NULL REFERENCES billing_tiers(id),
    
    -- Subscription details
    status VARCHAR(50) NOT NULL DEFAULT 'active', -- active, suspended, cancelled, trial
    billing_email VARCHAR(255),
    
    -- Subscription period
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    trial_ends_at TIMESTAMP,
    next_billing_date TIMESTAMP,
    cancelled_at TIMESTAMP,
    
    -- Custom arrangements (for enterprise tiers)
    custom_pricing JSONB DEFAULT '{}',       -- Custom pricing overrides
    custom_features JSONB DEFAULT '{}',      -- Custom feature overrides
    custom_allocations JSONB DEFAULT '{}',   -- Custom resource overrides
    
    -- Payment info
    payment_method VARCHAR(50),              -- stripe, mollie, crypto, manual, custom
    payment_reference VARCHAR(255),          -- External payment system reference
    
    -- Billing address and tax
    billing_address JSONB,
    tax_id VARCHAR(100),
    tax_rate DECIMAL(5,4) DEFAULT 0.0000,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(tenant_id) -- One active subscription per tenant
);

-- Tenant cluster assignments (which clusters tenant can use)
CREATE TABLE IF NOT EXISTS tenant_cluster_access (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL, -- References tenants(id)
    cluster_id VARCHAR(100) NOT NULL REFERENCES infrastructure_clusters(cluster_id),
    
    -- Access configuration
    access_level VARCHAR(50) DEFAULT 'shared', -- shared, dedicated, priority
    resource_limits JSONB DEFAULT '{}',        -- Tenant-specific limits on this cluster
    
    -- Usage tracking
    current_usage JSONB DEFAULT '{}',          -- Current usage stats
    quota_usage JSONB DEFAULT '{}',            -- Quota consumption
    
    -- Access control
    is_active BOOLEAN DEFAULT true,
    granted_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    
    UNIQUE(tenant_id, cluster_id)
); 

-- Aggregated usage records for billing (monthly, per usage_type)
CREATE TABLE IF NOT EXISTS usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    usage_type VARCHAR(50) NOT NULL,
    usage_value DECIMAL(15,6) NOT NULL DEFAULT 0,
    usage_details JSONB DEFAULT '{}',
    billing_month VARCHAR(7) NOT NULL, -- 'YYYY-MM'
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (tenant_id, cluster_id, usage_type, billing_month)
);

-- Invoice drafts generated from usage aggregation
CREATE TABLE IF NOT EXISTS invoice_drafts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    billing_period_start DATE NOT NULL,
    billing_period_end DATE NOT NULL,
    stream_hours DECIMAL(15,6) DEFAULT 0,
    egress_gb DECIMAL(15,6) DEFAULT 0,
    recording_gb DECIMAL(15,6) DEFAULT 0,
    max_viewers INTEGER DEFAULT 0,
    total_streams INTEGER DEFAULT 0,
    calculated_amount DECIMAL(15,6) DEFAULT 0,
    status VARCHAR(20) DEFAULT 'draft',
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (tenant_id, billing_period_start)
);

-- Insert actual billing tiers that match the pricing page
INSERT INTO billing_tiers (tier_name, display_name, description, base_price, currency, bandwidth_allocation, storage_allocation, compute_allocation, features, support_level, sla_level, metering_enabled, overage_rates, sort_order, is_enterprise) VALUES

-- Free Tier
('free', 'Free Tier', 'Self-hosted features with shared pool access', 0.00, 'EUR', 
'{"type": "shared_pool", "global_capacity_gbps": 100, "fair_use": true}',
'{"analytics_retention_days": 30, "recording_gb": 0}',
'{"gpu_access": false, "shared_cpu": true}',
'{"subdomain": false, "load_balancer": false, "ai_processing": false, "stream_dashboard": true, "basic_analytics": true, "self_hosted": true, "transcoding_livepeer": true}',
'community', 'none', false, '{}', 1, false),

-- Supporter Tier  
('supporter', 'Supporter', 'Enhanced features and processing access', 50.00, 'EUR',
'{"min_mbps": 100, "max_mbps": 250, "sustained_mbps": 200, "concurrent_viewers": 300}',
'{"analytics_retention_days": 90, "recording_gb": 50}', 
'{"gpu_access": false, "dedicated_cpu": false}',
'{"subdomain": true, "subdomain_pattern": "yourname.frameport.dev", "load_balancer": true, "calendar_integration": true, "stream_scheduling": true, "telemetry_monitoring": true, "basic_support": true}',
'basic', 'none', false, '{}', 2, false),

-- Developer Tier
('developer', 'Developer', 'Enhanced capacity for development teams', 250.00, 'EUR',
'{"min_mbps": 500, "max_mbps": 1000, "sustained_mbps": 750, "concurrent_viewers": 1000}',
'{"analytics_retention_days": 180, "recording_gb": 200}',
'{"gpu_access": true, "gpu_allocation": "shared", "ai_processing": true, "multi_stream_compositing": true}',
'{"subdomain": true, "team_collaboration": true, "priority_support": true, "advanced_analytics": true, "materialized_views": true}',
'priority', 'standard', false, '{}', 3, false),

-- Production Ready Tier  
('production', 'Production Ready', 'Reliable enterprise infrastructure with redundancy', 1000.00, 'EUR',
'{"min_gbps": 2, "max_gbps": 5, "sustained_gbps": 3, "concurrent_viewers": 5000}',
'{"analytics_retention_days": 365, "recording_gb": 1000}',
'{"gpu_access": true, "gpu_allocation": "dedicated", "processing_allocation": "dedicated"}', 
'{"subdomain": true, "enterprise_sla": true, "priority_support_24_7": true, "advanced_analytics": true, "live_dashboard": true, "redundancy": true}',
'enterprise', 'premium', true, '{"bandwidth_overage_per_gb": 0.02, "storage_overage_per_gb": 0.01}', 4, false),

-- Enterprise Tier
('enterprise', 'Enterprise', 'Custom solutions for massive scale operations', 0.00, 'EUR',
'{"unlimited": true, "custom_allocation": true}',
'{"unlimited": true, "custom_retention": true}',
'{"gpu_access": true, "gpu_allocation": "custom", "dedicated_infrastructure": true}',
'{"white_label": true, "custom_development": true, "private_deployment": true, "managed_service": true, "unlimited_bandwidth": true, "custom_sla": true, "training_consulting": true, "custom_billing": true}',
'dedicated', 'custom', true, '{"custom_rates": true}', 5, true)

ON CONFLICT (tier_name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    base_price = EXCLUDED.base_price,
    bandwidth_allocation = EXCLUDED.bandwidth_allocation,
    storage_allocation = EXCLUDED.storage_allocation,
    compute_allocation = EXCLUDED.compute_allocation,
    features = EXCLUDED.features,
    support_level = EXCLUDED.support_level,
    sla_level = EXCLUDED.sla_level,
    metering_enabled = EXCLUDED.metering_enabled,
    overage_rates = EXCLUDED.overage_rates,
    sort_order = EXCLUDED.sort_order,
    is_enterprise = EXCLUDED.is_enterprise; 

-- Flexible billing system indexes
CREATE INDEX IF NOT EXISTS idx_billing_tiers_active ON billing_tiers(is_active, sort_order);
CREATE INDEX IF NOT EXISTS idx_billing_tiers_enterprise ON billing_tiers(is_enterprise);
CREATE INDEX IF NOT EXISTS idx_cluster_tier_support_cluster ON cluster_tier_support(cluster_id);
CREATE INDEX IF NOT EXISTS idx_cluster_tier_support_tier ON cluster_tier_support(tier_id);
CREATE INDEX IF NOT EXISTS idx_cluster_tier_support_available ON cluster_tier_support(is_available);
CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_tenant ON tenant_subscriptions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_tier ON tenant_subscriptions(tier_id);
CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_status ON tenant_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_billing_date ON tenant_subscriptions(next_billing_date);
CREATE INDEX IF NOT EXISTS idx_tenant_cluster_access_tenant ON tenant_cluster_access(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_cluster_access_cluster ON tenant_cluster_access(cluster_id);
CREATE INDEX IF NOT EXISTS idx_tenant_cluster_access_active ON tenant_cluster_access(is_active);

-- Billing aggregation indexes
CREATE INDEX IF NOT EXISTS idx_usage_records_lookup ON usage_records(tenant_id, cluster_id, usage_type, billing_month);
CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records(created_at);
CREATE INDEX IF NOT EXISTS idx_invoice_drafts_tenant ON invoice_drafts(tenant_id, billing_period_start);
CREATE INDEX IF NOT EXISTS idx_invoice_drafts_status ON invoice_drafts(status);

-- =====================================================================
-- DEMO DATA INITIALIZATION (after all tables are created)
-- =====================================================================

-- Set up demo tenant subscription using new flexible billing system
INSERT INTO tenant_subscriptions (tenant_id, tier_id, status, billing_email, started_at, next_billing_date) 
SELECT '00000000-0000-0000-0000-000000000001', bt.id, 'active', 'demo@frameworks.dev', NOW(), NOW() + INTERVAL '1 month'
FROM billing_tiers bt WHERE bt.tier_name = 'developer'
ON CONFLICT (tenant_id) DO UPDATE SET
    tier_id = EXCLUDED.tier_id,
    status = EXCLUDED.status,
    billing_email = EXCLUDED.billing_email;

-- Grant demo tenant access to central-primary cluster
INSERT INTO tenant_cluster_access (tenant_id, cluster_id, access_level, resource_limits, is_active)
VALUES ('00000000-0000-0000-0000-000000000001', 'central-primary', 'shared', '{"bandwidth_limit_mbps": 1000}', true)
ON CONFLICT (tenant_id, cluster_id) DO UPDATE SET
    access_level = EXCLUDED.access_level,
    resource_limits = EXCLUDED.resource_limits,
    is_active = EXCLUDED.is_active; 