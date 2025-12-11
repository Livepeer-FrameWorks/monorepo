-- ============================================================================
-- PURSER SCHEMA - BILLING & PAYMENT PROCESSING
-- ============================================================================
-- Manages billing invoices, payments, crypto wallets, tiers, and usage tracking
-- Core financial operations for tenant subscription and metered billing
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS purser;

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================================
-- BILLING & INVOICING
-- ============================================================================

-- Invoice generation and payment tracking
CREATE TABLE IF NOT EXISTS purser.billing_invoices (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,

    -- ===== FINANCIAL DETAILS =====
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, paid, overdue, cancelled
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    amount DECIMAL(10,2) NOT NULL DEFAULT 0,

    -- ===== PAYMENT TIMELINE =====
    due_date TIMESTAMP WITH TIME ZONE NOT NULL,
    paid_at TIMESTAMP WITH TIME ZONE,

    -- ===== AMOUNT BREAKDOWN =====
    base_amount DECIMAL(10,2) NOT NULL DEFAULT 0,    -- Subscription base fee
    metered_amount DECIMAL(10,2) NOT NULL DEFAULT 0, -- Usage-based charges
    usage_details JSONB NOT NULL DEFAULT '{}',       -- Detailed usage breakdown

    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Payment transactions against invoices
CREATE TABLE IF NOT EXISTS purser.billing_payments (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    
    -- ===== PAYMENT DETAILS =====
    method VARCHAR(50) NOT NULL, -- crypto, stripe, bank_transfer
    amount DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    tx_id VARCHAR(255), -- External transaction ID
    
    -- ===== STATUS =====
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, confirmed, failed
    confirmed_at TIMESTAMP WITH TIME ZONE,
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- ============================================================================
-- CRYPTOCURRENCY PAYMENT INFRASTRUCTURE
-- ============================================================================

-- Temporary crypto wallets for invoice payments
CREATE TABLE IF NOT EXISTS purser.crypto_wallets (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    
    -- ===== WALLET DETAILS =====
    asset VARCHAR(10) NOT NULL,        -- BTC, ETH, USDC, etc.
    wallet_address VARCHAR(255) NOT NULL,
    
    -- ===== STATUS & LIFECYCLE =====
    status VARCHAR(50) NOT NULL DEFAULT 'active', -- active, expired, used
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(invoice_id, asset)
);

CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_active ON purser.crypto_wallets(status, expires_at);

-- ============================================================================
-- BILLING TIERS & SUBSCRIPTION PLANS
-- ============================================================================

-- Service tier definitions with pricing and resource allocations
CREATE TABLE IF NOT EXISTS purser.billing_tiers (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_name VARCHAR(100) NOT NULL UNIQUE,
    display_name VARCHAR(255) NOT NULL,
    description TEXT,
    
    -- ===== PRICING =====
    base_price DECIMAL(10,2) NOT NULL DEFAULT 0.00,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    billing_period VARCHAR(20) NOT NULL DEFAULT 'monthly',
    
    -- ===== RESOURCE ALLOCATIONS =====
    bandwidth_allocation JSONB DEFAULT '{}', -- Bandwidth limits and guarantees
    storage_allocation JSONB DEFAULT '{}',   -- Storage quotas and retention
    compute_allocation JSONB DEFAULT '{}',   -- CPU/GPU/processing limits
    
    -- ===== FEATURES & SUPPORT =====
    features JSONB NOT NULL DEFAULT '{}',    -- Feature flags and capabilities
    support_level VARCHAR(50) DEFAULT 'community',
    sla_level VARCHAR(50) DEFAULT 'none',
    
    -- ===== METERING & OVERAGES =====
    metering_enabled BOOLEAN DEFAULT false,
    overage_rates JSONB DEFAULT '{}',        -- Per-unit overage pricing
    
    -- ===== STATUS & ORDERING =====
    is_active BOOLEAN DEFAULT true,
    sort_order INTEGER DEFAULT 0,
    is_enterprise BOOLEAN DEFAULT false,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Cluster-specific tier availability and configuration
CREATE TABLE IF NOT EXISTS purser.cluster_tier_support (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL,
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id),
    
    -- ===== CLUSTER-SPECIFIC CONFIGURATION =====
    tier_config JSONB DEFAULT '{}',          -- Cluster-specific overrides
    capacity_allocation DECIMAL(5,2) DEFAULT 100.00, -- % of cluster capacity
    priority_level INTEGER DEFAULT 0,        -- Resource priority
    
    -- ===== AVAILABILITY =====
    is_available BOOLEAN DEFAULT true,
    effective_from TIMESTAMP DEFAULT NOW(),
    effective_until TIMESTAMP,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(cluster_id, tier_id)
);

-- ============================================================================
-- TENANT SUBSCRIPTIONS
-- ============================================================================

-- Active subscriptions linking tenants to billing tiers
CREATE TABLE IF NOT EXISTS purser.tenant_subscriptions (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id),
    
    -- ===== SUBSCRIPTION STATUS =====
    status VARCHAR(50) NOT NULL DEFAULT 'active', -- active, cancelled, suspended
    billing_email VARCHAR(255),
    
    -- ===== SUBSCRIPTION LIFECYCLE =====
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    trial_ends_at TIMESTAMP,
    next_billing_date TIMESTAMP,
    cancelled_at TIMESTAMP,
    
    -- ===== CUSTOMIZATION =====
    custom_pricing JSONB DEFAULT '{}',       -- Custom pricing overrides
    custom_features JSONB DEFAULT '{}',      -- Custom feature flags
    custom_allocations JSONB DEFAULT '{}',   -- Custom resource limits
    
    -- ===== PAYMENT & BILLING =====
    payment_method VARCHAR(50),
    payment_reference VARCHAR(255),
    billing_address JSONB,
    tax_id VARCHAR(100),
    tax_rate DECIMAL(5,4) DEFAULT 0.0000,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- USAGE TRACKING & METERING
-- ============================================================================

-- Aggregated usage records for billing calculations
CREATE TABLE IF NOT EXISTS purser.usage_records (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    
    -- ===== USAGE METRICS =====
    usage_type VARCHAR(50) NOT NULL,         -- stream_hours, egress_gb, storage_gb
    usage_value DECIMAL(15,6) NOT NULL DEFAULT 0,
    usage_details JSONB DEFAULT '{}',        -- Additional usage metadata
    
    -- ===== BILLING PERIOD =====
    billing_month VARCHAR(7) NOT NULL,       -- YYYY-MM format
    period_start TIMESTAMP WITH TIME ZONE,   -- Granular start time
    period_end TIMESTAMP WITH TIME ZONE,     -- Granular end time

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (tenant_id, cluster_id, usage_type, period_start, period_end)
);

-- Invoice draft calculations before final invoice generation
CREATE TABLE IF NOT EXISTS purser.invoice_drafts (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    
    -- ===== BILLING PERIOD =====
    billing_period_start DATE NOT NULL,
    billing_period_end DATE NOT NULL,
    
    -- ===== USAGE SUMMARY =====
    stream_hours DECIMAL(15,6) DEFAULT 0,
    egress_gb DECIMAL(15,6) DEFAULT 0,
    recording_gb DECIMAL(15,6) DEFAULT 0,
    max_viewers INTEGER DEFAULT 0,
    total_streams INTEGER DEFAULT 0,
    usage_details JSONB NOT NULL DEFAULT '{}',
    
    -- ===== CALCULATED BILLING =====
    calculated_amount DECIMAL(15,6) DEFAULT 0,
    status VARCHAR(20) DEFAULT 'draft', -- draft, finalized, invoiced
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (tenant_id, billing_period_start)
);

-- ============================================================================
-- USAGE TRACKING INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_lookup ON purser.usage_records(tenant_id, cluster_id, usage_type, billing_month);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_created_at ON purser.usage_records(created_at);

-- ============================================================================
-- BILLING TIER SEED DATA
-- ============================================================================
-- See seeds/static/purser_tiers.sql for production values
--
-- AllocationDetails: {"limit": <float|null>, "unit": "<string>", "unit_price": <float>}
-- OverageRates: {"bandwidth": {...}, "storage": {...}, "compute": {...}}
-- BillingFeatures: {"recording": bool, "analytics": bool, "api_access": bool, ...}
--
-- NOTE: Enforcement limits (max_streams, max_viewers, Mbps) belong in quartermaster
-- ============================================================================

INSERT INTO purser.billing_tiers (tier_name, display_name, description, base_price, currency, bandwidth_allocation, storage_allocation, compute_allocation, features, support_level, sla_level, metering_enabled, overage_rates, sort_order, is_enterprise) VALUES

-- Free Tier
('free', 'Free Tier', 'Self-hosted features & free access to transcoding and shared bandwidth pool.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": 30, "unit": "retention_days", "unit_price": 0}',
'{"limit": 0, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": false, "analytics": true, "api_access": true, "support_level": "community"}',
'community', 'none', false,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}}',
1, false),

-- Supporter Tier (€79/mo)
('supporter', 'Supporter', 'Starter allowances with hosted load balancer and custom subdomain.', 79.00, 'EUR',
'{"limit": 150000, "unit": "delivered_minutes", "unit_price": 0.00049}',
'{"limit": 90, "unit": "retention_days", "unit_price": 0}',
'{"limit": 10, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "basic"}',
'basic', 'none', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00049}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.01}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
2, false),

-- Developer Tier (€249/mo)
('developer', 'Developer', 'Expanded allowances, collaboration tooling, and shared GPU priority.', 249.00, 'EUR',
'{"limit": 500000, "unit": "delivered_minutes", "unit_price": 0.00047}',
'{"limit": 180, "unit": "retention_days", "unit_price": 0}',
'{"limit": 50, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "support_level": "priority"}',
'priority', 'standard', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00047}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.008}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
3, false),

-- Production Tier (€999/mo)
('production', 'Production', 'High allowances, dedicated options, and enterprise support.', 999.00, 'EUR',
'{"limit": 2000000, "unit": "delivered_minutes", "unit_price": 0.00045}',
'{"limit": 365, "unit": "retention_days", "unit_price": 0}',
'{"limit": 250, "unit": "gpu_hours", "unit_price": 0.50}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "enterprise"}',
'enterprise', 'premium', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0.00045}, "storage": {"limit": null, "unit": "gb", "unit_price": 0.005}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0.50}}',
4, false),

-- Enterprise Tier (custom pricing)
('enterprise', 'Enterprise', 'Custom solutions for massive scale operations.', 0.00, 'EUR',
'{"limit": null, "unit": "delivered_minutes", "unit_price": 0}',
'{"limit": null, "unit": "retention_days", "unit_price": 0}',
'{"limit": null, "unit": "gpu_hours", "unit_price": 0}',
'{"recording": true, "analytics": true, "api_access": true, "custom_branding": true, "sla": true, "support_level": "dedicated"}',
'dedicated', 'custom', true,
'{"bandwidth": {"limit": null, "unit": "delivered_minutes", "unit_price": 0}, "storage": {"limit": null, "unit": "gb", "unit_price": 0}, "compute": {"limit": null, "unit": "gpu_hours", "unit_price": 0}}',
5, true)

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

-- ============================================================================
-- BILLING & SUBSCRIPTION INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_active ON purser.billing_tiers(is_active, sort_order);
CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_enterprise ON purser.billing_tiers(is_enterprise);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_tier_support_cluster ON purser.cluster_tier_support(cluster_id);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_tier_support_tier ON purser.cluster_tier_support(tier_id);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_tier_support_available ON purser.cluster_tier_support(is_available);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_tenant ON purser.tenant_subscriptions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_tier ON purser.tenant_subscriptions(tier_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_status ON purser.tenant_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_billing_date ON purser.tenant_subscriptions(next_billing_date);
