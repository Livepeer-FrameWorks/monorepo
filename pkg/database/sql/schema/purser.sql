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
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- draft, pending, paid, overdue, cancelled
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    amount DECIMAL(10,2) NOT NULL DEFAULT 0,

    -- ===== BILLING PERIOD =====
    period_start TIMESTAMP WITH TIME ZONE,
    period_end TIMESTAMP WITH TIME ZONE,

    -- ===== PAYMENT TIMELINE =====
    due_date TIMESTAMP WITH TIME ZONE NOT NULL,
    paid_at TIMESTAMP WITH TIME ZONE,

    -- ===== AMOUNT BREAKDOWN =====
    base_amount DECIMAL(10,2) NOT NULL DEFAULT 0,    -- Subscription base fee
    metered_amount DECIMAL(10,2) NOT NULL DEFAULT 0, -- Usage-based charges
    prepaid_credit_applied DECIMAL(10,2) NOT NULL DEFAULT 0, -- Credit applied from prepaid balance
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
    actual_tx_amount DECIMAL(30,18),
    asset_type VARCHAR(10),
    network VARCHAR(20),
    block_number BIGINT,
    
    -- ===== STATUS =====
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, confirmed, failed
    confirmed_at TIMESTAMP WITH TIME ZONE,
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- ============================================================================
-- CRYPTOCURRENCY PAYMENT INFRASTRUCTURE
-- ============================================================================
-- Unified deposit wallet system for both invoice payments and prepaid top-ups.
-- ETH-network only (ETH, USDC, LPT) for simplicity - same sweep infrastructure.
-- Uses HD wallet derivation from xpub - private keys never touch the server.

-- HD wallet state: tracks next derivation index for address generation
-- Single row table - one xpub, monotonically increasing index
CREATE TABLE IF NOT EXISTS purser.hd_wallet_state (
    id INTEGER PRIMARY KEY DEFAULT 1,
    -- Extended public key (xpub) - derived from master seed offline
    -- Format: xpub... (BIP32 serialized)
    -- Server can derive child addresses but NOT sign transactions
    xpub TEXT NOT NULL,

    -- Next derivation index to use (BIP44: m/44'/60'/0'/0/{index})
    -- Incremented atomically when generating new addresses
    next_index INTEGER NOT NULL DEFAULT 0,

    -- Network: mainnet or testnet (affects derivation path)
    network VARCHAR(20) NOT NULL DEFAULT 'mainnet',
    CONSTRAINT chk_network CHECK (network IN ('mainnet', 'testnet')),

    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),

    -- Ensure single row
    CONSTRAINT single_row CHECK (id = 1)
);

CREATE TABLE IF NOT EXISTS purser.crypto_wallets (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,

    -- ===== PURPOSE: invoice payment OR prepaid top-up =====
    purpose VARCHAR(20) NOT NULL DEFAULT 'invoice',
    CONSTRAINT chk_wallet_purpose CHECK (purpose IN ('invoice', 'prepaid')),

    -- For invoice payments (NULL for prepaid)
    invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,

    -- For prepaid top-ups: expected amount in cents (NULL for invoice - amount comes from invoice)
    expected_amount_cents BIGINT,

    -- ===== WALLET DETAILS =====
    -- Asset type: ETH (native), USDC (ERC-20), LPT (ERC-20)
    asset VARCHAR(10) NOT NULL,
    CONSTRAINT chk_wallet_asset CHECK (asset IN ('ETH', 'USDC', 'LPT')),

    -- Network: ethereum, base, arbitrum (+ testnets)
    network VARCHAR(20) NOT NULL DEFAULT 'ethereum',
    CONSTRAINT chk_wallet_network CHECK (network IN ('ethereum', 'base', 'arbitrum', 'base-sepolia', 'arbitrum-sepolia')),

    wallet_address VARCHAR(255) NOT NULL,

    -- HD wallet derivation index (from xpub) - enables address regeneration
    derivation_index INTEGER NOT NULL,

    -- ===== STATUS & LIFECYCLE =====
    -- active: awaiting payment
    -- used: payment received, pending sweep
    -- swept: funds moved to cold storage
    -- expired: no payment received before expiry
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    CONSTRAINT chk_wallet_status CHECK (status IN ('active', 'used', 'swept', 'expired')),

    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    confirmed_tx_hash VARCHAR(66),
    actual_amount_received DECIMAL(30,18),
    block_number BIGINT,
    confirmed_at TIMESTAMP WITH TIME ZONE,

    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),

    -- Constraints:
    -- 1. Invoice wallets must have invoice_id
    -- 2. Prepaid wallets must have expected_amount_cents
    CONSTRAINT chk_invoice_wallet CHECK (
        purpose != 'invoice' OR invoice_id IS NOT NULL
    ),
    CONSTRAINT chk_prepaid_wallet CHECK (
        purpose != 'prepaid' OR expected_amount_cents IS NOT NULL
    ),

    -- Unique constraints:
    -- For invoices: one wallet per invoice+asset
    -- For prepaid: derivation_index must be unique (global address pool)
    UNIQUE(invoice_id, asset),
    UNIQUE(derivation_index)
);

CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_active ON purser.crypto_wallets(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_tenant ON purser.crypto_wallets(tenant_id, purpose);
CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_address ON purser.crypto_wallets(wallet_address);
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_crypto_wallets_confirmed_tx
    ON purser.crypto_wallets(network, confirmed_tx_hash)
    WHERE confirmed_tx_hash IS NOT NULL;

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
    
    -- ===== STATUS & TIER LEVEL =====
    is_active BOOLEAN DEFAULT true,
    tier_level INTEGER DEFAULT 0,
    is_enterprise BOOLEAN DEFAULT false,
    
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
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

    -- ===== BILLING MODEL =====
    -- postpaid: Traditional invoicing (use resources, pay later)
    -- prepaid: Balance-based (pay first, deduct on usage)
    -- Wallet-only accounts MUST use prepaid (enforced at account creation)
    billing_model VARCHAR(20) NOT NULL DEFAULT 'postpaid',

    -- ===== SUBSCRIPTION LIFECYCLE =====
    started_at TIMESTAMP NOT NULL DEFAULT NOW(),
    trial_ends_at TIMESTAMP,
    next_billing_date TIMESTAMP,
    billing_period_start TIMESTAMP,
    billing_period_end TIMESTAMP,
    cancelled_at TIMESTAMP,

    -- ===== CUSTOMIZATION =====
    custom_pricing JSONB DEFAULT '{}',       -- Custom pricing overrides
    custom_features JSONB DEFAULT '{}',      -- Custom feature flags
    custom_allocations JSONB DEFAULT '{}',   -- Custom resource limits

    -- ===== PAYMENT & BILLING =====
    payment_method VARCHAR(50),
    payment_reference VARCHAR(255),
    billing_company VARCHAR(255),         -- Company name for invoices
    billing_address JSONB,                -- Structured: {line1, line2, city, postalCode, country}
    tax_id VARCHAR(100),                  -- VAT number (EU format: XX123456789)
    tax_rate DECIMAL(5,4) DEFAULT 0.0000,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT chk_billing_model CHECK (billing_model IN ('postpaid', 'prepaid')),
    UNIQUE(tenant_id)
);

-- ============================================================================
-- PREPAID BALANCE SYSTEM
-- ============================================================================
-- Balance-based billing for wallet accounts and agents
-- Supports pay-first, use-later model with crypto top-ups
-- ============================================================================

-- Current balance per tenant (one row per tenant/currency pair)
CREATE TABLE IF NOT EXISTS purser.prepaid_balances (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,

    -- ===== BALANCE STATE =====
    balance_cents BIGINT NOT NULL DEFAULT 0,          -- Current balance in cents
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',

    -- ===== ALERTS =====
    low_balance_threshold_cents BIGINT DEFAULT 500,   -- Alert when below $5

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),

    UNIQUE(tenant_id, currency)
);

-- Transaction history for audit trail and debugging
CREATE TABLE IF NOT EXISTS purser.balance_transactions (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,

    -- ===== TRANSACTION DETAILS =====
    amount_cents BIGINT NOT NULL,                     -- Positive = top-up, negative = usage
    balance_after_cents BIGINT NOT NULL,              -- Balance after this transaction
    transaction_type VARCHAR(20) NOT NULL,            -- topup, usage, refund, adjustment
    description TEXT,

    -- ===== REFERENCES =====
    reference_id UUID,                                -- Links to crypto payment, usage record, etc.
    reference_type VARCHAR(50),                       -- crypto_payment, usage_record, manual_adjustment

    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_prepaid_balances_tenant ON purser.prepaid_balances(tenant_id);
CREATE INDEX IF NOT EXISTS idx_balance_transactions_tenant ON purser.balance_transactions(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_balance_transactions_type ON purser.balance_transactions(transaction_type, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_balance_transactions_idempotency
    ON purser.balance_transactions(tenant_id, reference_type, reference_id)
    WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL;

-- ============================================================================
-- PENDING TOP-UPS (Card Payments)
-- ============================================================================
-- Tracks checkout sessions for card-based prepaid balance top-ups.
-- Flow: User requests top-up → Stripe/Mollie checkout session created →
--       User pays → Webhook fires → Balance credited → Top-up marked complete.
-- ============================================================================

CREATE TABLE IF NOT EXISTS purser.pending_topups (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,

    -- ===== PAYMENT PROVIDER =====
    provider VARCHAR(20) NOT NULL,                -- stripe, mollie
    CONSTRAINT chk_topup_provider CHECK (provider IN ('stripe', 'mollie')),

    -- Provider-specific checkout/payment session ID
    -- Stripe: checkout session ID (cs_xxx)
    -- Mollie: payment ID (tr_xxx)
    checkout_id VARCHAR(255) NOT NULL,

    -- ===== AMOUNT =====
    amount_cents BIGINT NOT NULL,                 -- Amount to credit on success
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',

    -- ===== STATUS & LIFECYCLE =====
    -- pending: checkout created, awaiting payment
    -- completed: payment received, balance credited
    -- failed: payment failed or cancelled by user
    -- expired: checkout session expired without payment
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    CONSTRAINT chk_topup_status CHECK (status IN ('pending', 'completed', 'failed', 'expired')),

    -- When the checkout session expires (Stripe: 24h default, Mollie: configurable)
    expires_at TIMESTAMPTZ NOT NULL,

    -- When the payment was completed (NULL until status = completed)
    completed_at TIMESTAMPTZ,

    -- Reference to balance_transaction created on completion
    balance_transaction_id UUID REFERENCES purser.balance_transactions(id),

    -- ===== BILLING DETAILS (optional) =====
    -- Captured from checkout for invoice generation
    billing_email VARCHAR(255),
    billing_name VARCHAR(255),
    billing_company VARCHAR(255),
    billing_vat_number VARCHAR(50),
    billing_address JSONB,

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),

    -- Unique constraint: one pending checkout per provider session
    UNIQUE(provider, checkout_id)
);

CREATE INDEX IF NOT EXISTS idx_pending_topups_tenant ON purser.pending_topups(tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_pending_topups_checkout ON purser.pending_topups(provider, checkout_id);
CREATE INDEX IF NOT EXISTS idx_pending_topups_status ON purser.pending_topups(status, expires_at);

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
    period_start TIMESTAMP WITH TIME ZONE,   -- Granular start time
    period_end TIMESTAMP WITH TIME ZONE,     -- Granular end time
    granularity VARCHAR(20) NOT NULL DEFAULT 'hourly', -- hourly, daily, monthly

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (tenant_id, cluster_id, usage_type, period_start, period_end)
);

-- ============================================================================
-- USAGE TRACKING INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_lookup ON purser.usage_records(tenant_id, cluster_id, usage_type);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_created_at ON purser.usage_records(created_at);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_period ON purser.usage_records(tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_granularity_period ON purser.usage_records(tenant_id, granularity, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_purser_billing_invoices_period ON purser.billing_invoices(tenant_id, status, period_start);

CREATE INDEX IF NOT EXISTS idx_purser_billing_invoices_tenant_status_due ON purser.billing_invoices(tenant_id, status, due_date);
CREATE INDEX IF NOT EXISTS idx_purser_billing_payments_invoice_id ON purser.billing_payments(invoice_id);
CREATE INDEX IF NOT EXISTS idx_purser_billing_payments_tx_id ON purser.billing_payments(tx_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_payments_tx_unique
    ON purser.billing_payments(tx_id)
    WHERE tx_id IS NOT NULL;

-- ============================================================================
-- BILLING & SUBSCRIPTION INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_active ON purser.billing_tiers(is_active, tier_level);
CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_enterprise ON purser.billing_tiers(is_enterprise);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_tenant ON purser.tenant_subscriptions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_tier ON purser.tenant_subscriptions(tier_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_status ON purser.tenant_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_billing_date ON purser.tenant_subscriptions(next_billing_date);

-- ============================================================================
-- CLUSTER MARKETPLACE PRICING
-- ============================================================================
-- Per-cluster pricing configuration for marketplace clusters
-- Supports independent pricing models, Stripe integration, and tier requirements
-- ============================================================================

CREATE TABLE IF NOT EXISTS purser.cluster_pricing (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL UNIQUE,

    -- ===== PRICING MODEL =====
    -- Determines how this cluster bills subscribers:
    --   'free_unmetered'  - No charge, quota enforcement only
    --   'metered'         - Usage-based billing (bandwidth/minutes/storage)
    --   'monthly'         - Fixed monthly subscription fee
    --   'tier_inherit'    - Inherits pricing from subscriber's billing tier (default)
    --   'custom'          - Per-agreement enterprise pricing
    pricing_model VARCHAR(20) NOT NULL DEFAULT 'tier_inherit',

    -- ===== STRIPE INTEGRATION =====
    stripe_product_id VARCHAR(255),        -- Stripe Product ID for this cluster
    stripe_price_id_monthly VARCHAR(255),  -- Stripe Price ID for monthly model
    stripe_meter_id VARCHAR(255),          -- Stripe Billing Meter ID for metered model

    -- ===== BASE PRICING (for 'monthly' model) =====
    base_price DECIMAL(10,2) DEFAULT 0.00,
    currency VARCHAR(3) DEFAULT 'EUR',

    -- ===== METERED RATES (override tenant tier rates) =====
    -- Format: {"delivered_minutes": 0.0005, "storage_gb": 0.01, "egress_gb": 0.02}
    metered_rates JSONB DEFAULT '{}',

    -- ===== VISIBILITY & ACCESS RULES =====
    -- Minimum tier level required to see/subscribe to this cluster
    -- 0=no subscription required, 1=free, 2=supporter, 3=developer, 4=production, 5=enterprise
    required_tier_level INT DEFAULT 0,
    is_platform_official BOOLEAN DEFAULT FALSE,  -- Platform-operated cluster
    allow_free_tier BOOLEAN DEFAULT FALSE,       -- If platform_official, allow free tier access

    -- ===== DEFAULT QUOTAS (for free_unmetered or as caps) =====
    -- Format: {"max_streams": 2, "max_viewers": 50, "max_bandwidth_mbps": 100, "retention_days": 7}
    default_quotas JSONB DEFAULT '{}',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT chk_cluster_pricing_model CHECK (pricing_model IN ('free_unmetered', 'metered', 'monthly', 'tier_inherit', 'custom'))
);

CREATE INDEX IF NOT EXISTS idx_purser_cluster_pricing_model ON purser.cluster_pricing(pricing_model);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_pricing_platform ON purser.cluster_pricing(is_platform_official);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_pricing_tier_level ON purser.cluster_pricing(required_tier_level);

-- ============================================================================
-- STRIPE SUBSCRIPTION TRACKING
-- ============================================================================
-- Columns added to tenant_subscriptions for Stripe integration.
-- These track Stripe customer, subscription, and billing cycle state.
-- ============================================================================

ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS stripe_customer_id VARCHAR(255);
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS stripe_subscription_id VARCHAR(255);
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS stripe_subscription_status VARCHAR(50);
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS stripe_current_period_end TIMESTAMPTZ;
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS dunning_attempts INT DEFAULT 0;

-- Stripe price IDs on billing tiers for checkout session creation
ALTER TABLE purser.billing_tiers ADD COLUMN IF NOT EXISTS stripe_price_id_monthly VARCHAR(255);
ALTER TABLE purser.billing_tiers ADD COLUMN IF NOT EXISTS stripe_price_id_yearly VARCHAR(255);
ALTER TABLE purser.billing_tiers ADD COLUMN IF NOT EXISTS stripe_product_id VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_stripe_customer ON purser.tenant_subscriptions(stripe_customer_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_stripe_subscription ON purser.tenant_subscriptions(stripe_subscription_id);

-- ============================================================================
-- MOLLIE SUBSCRIPTION TRACKING
-- ============================================================================
-- Mollie uses mandates (SEPA Direct Debit, credit card) for recurring payments.
-- First payment (e.g., iDEAL) creates a mandate, then subscriptions auto-charge.
-- ============================================================================

-- Mollie customer mapping (one Mollie customer per tenant)
CREATE TABLE IF NOT EXISTS purser.mollie_customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL UNIQUE,
    mollie_customer_id VARCHAR(50) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Mollie mandates (payment authorization for recurring charges)
CREATE TABLE IF NOT EXISTS purser.mollie_mandates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    mollie_customer_id VARCHAR(50) NOT NULL,
    mollie_mandate_id VARCHAR(50) NOT NULL UNIQUE,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending, valid, invalid, revoked
    method VARCHAR(50) NOT NULL,                     -- directdebit, creditcard, ideal (first payment only)
    details JSONB DEFAULT '{}',                      -- Bank account/card details from Mollie
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Mollie subscription ID on tenant_subscriptions
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS mollie_subscription_id VARCHAR(50);

CREATE INDEX IF NOT EXISTS idx_purser_mollie_customers_tenant ON purser.mollie_customers(tenant_id);
CREATE INDEX IF NOT EXISTS idx_purser_mollie_mandates_tenant ON purser.mollie_mandates(tenant_id);
CREATE INDEX IF NOT EXISTS idx_purser_mollie_mandates_customer ON purser.mollie_mandates(mollie_customer_id);
CREATE INDEX IF NOT EXISTS idx_purser_mollie_mandates_status ON purser.mollie_mandates(status);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_mollie ON purser.tenant_subscriptions(mollie_subscription_id);

-- ============================================================================
-- CLUSTER SUBSCRIPTION TRACKING (PAID CLUSTERS)
-- ============================================================================
-- Tracks Stripe subscriptions for paid cluster access.
-- ============================================================================ 

CREATE TABLE IF NOT EXISTS purser.cluster_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending_payment', -- active, pending_payment, cancelled, past_due, suspended
    stripe_customer_id VARCHAR(255),
    stripe_subscription_id VARCHAR(255),
    stripe_subscription_status VARCHAR(50),
    stripe_current_period_end TIMESTAMPTZ,
    checkout_session_id VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    cancelled_at TIMESTAMPTZ,
    UNIQUE(tenant_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_purser_cluster_subscriptions_tenant ON purser.cluster_subscriptions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_subscriptions_cluster ON purser.cluster_subscriptions(cluster_id);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_subscriptions_stripe_sub ON purser.cluster_subscriptions(stripe_subscription_id);

-- ============================================================================
-- WEBHOOK IDEMPOTENCY
-- ============================================================================
-- Prevents duplicate processing of webhooks (Stripe/Mollie may retry).
-- Each webhook event is recorded by provider + event_id (e.g., Stripe evt_xxx).
-- ============================================================================

CREATE TABLE IF NOT EXISTS purser.webhook_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider VARCHAR(50) NOT NULL,           -- stripe, mollie
    event_id VARCHAR(255) NOT NULL,          -- Provider's unique event identifier
    event_type VARCHAR(100),                 -- checkout.session.completed, etc.
    processed_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(provider, event_id)
);

CREATE INDEX IF NOT EXISTS idx_purser_webhook_events_provider ON purser.webhook_events(provider, processed_at DESC);

-- ============================================================================
-- X402 PROTOCOL SUPPORT
-- ============================================================================
-- EIP-3009 "Transfer With Authorization" for programmatic USDC payments.
-- Enables AI agents to top up balance without human intervention.
-- Only USDC/EURC supported (EIP-3009 tokens). ETH/LPT use deposit flow.
-- ============================================================================

-- Per-tenant x402 address index (reuse same address across payments)
ALTER TABLE purser.tenant_subscriptions ADD COLUMN IF NOT EXISTS x402_address_index INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_subscriptions_x402_address_index
    ON purser.tenant_subscriptions(x402_address_index)
    WHERE x402_address_index IS NOT NULL;

-- Nonce tracking to prevent replay attacks
-- Each EIP-3009 authorization has a unique nonce that can only be used once
CREATE TABLE IF NOT EXISTS purser.x402_nonces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Network + payer address + nonce = unique authorization
    network VARCHAR(20) NOT NULL,                 -- base, base-sepolia
    payer_address VARCHAR(42) NOT NULL,           -- 0x-prefixed address that signed
    nonce VARCHAR(78) NOT NULL,                   -- uint256 as hex string

    -- Settlement details
    tx_hash VARCHAR(66) NOT NULL,                 -- Transaction hash (0x + 64 hex)
    tenant_id UUID NOT NULL,                      -- Tenant that received credit
    amount_cents BIGINT NOT NULL,                 -- Amount credited in cents
    settled_at TIMESTAMPTZ DEFAULT NOW(),

    -- Reconciliation: track on-chain confirmation
    -- pending: tx submitted, balance credited optimistically
    -- confirmed: tx confirmed on-chain (receipt.status = 1)
    -- failed: tx reverted or timed out, balance debited
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    confirmed_at TIMESTAMPTZ,
    block_number BIGINT,
    gas_used BIGINT,
    failure_reason TEXT,

    UNIQUE(network, payer_address, nonce)
);

CREATE INDEX IF NOT EXISTS idx_x402_nonces_tenant ON purser.x402_nonces(tenant_id);
CREATE INDEX IF NOT EXISTS idx_x402_nonces_payer ON purser.x402_nonces(payer_address, network);
CREATE INDEX IF NOT EXISTS idx_x402_nonces_pending ON purser.x402_nonces(status, settled_at) WHERE status = 'pending';

-- ============================================================================
-- SIMPLIFIED INVOICES (EU VAT COMPLIANCE)
-- ============================================================================
-- For payments under €100, EU law allows simplified invoices without
-- customer details. We store these for 10-year retention requirement.
-- ============================================================================

CREATE TABLE IF NOT EXISTS purser.simplified_invoices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Invoice reference
    invoice_number VARCHAR(50) NOT NULL UNIQUE,   -- Sequential: SI-2024-000001

    -- Tenant (internal reference only, not on invoice)
    tenant_id UUID NOT NULL,

    -- Transaction reference
    reference_type VARCHAR(20) NOT NULL,          -- x402, crypto, card
    reference_id VARCHAR(255) NOT NULL,           -- tx_hash or payment ID

    -- Amounts (all in cents)
    gross_amount_cents BIGINT NOT NULL,           -- Total including VAT
    net_amount_cents BIGINT NOT NULL,             -- Amount before VAT
    vat_amount_cents BIGINT NOT NULL,             -- VAT portion
    vat_rate_bps INTEGER NOT NULL,                -- VAT rate in basis points (2100 = 21%)
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',

    -- EUR equivalent (for threshold tracking)
    amount_eur_cents BIGINT NOT NULL,             -- Converted at ECB rate
    ecb_rate DECIMAL(10,6),                       -- EUR/USD rate used

    -- Location evidence (2 pieces required for EU VAT)
    evidence_ip_country VARCHAR(2),               -- ISO country from IP
    evidence_wallet_network VARCHAR(20),          -- Blockchain network

    -- Supplier details (us - static but stored for audit)
    supplier_name VARCHAR(255) NOT NULL,
    supplier_address TEXT NOT NULL,
    supplier_vat_number VARCHAR(50) NOT NULL,

    -- Invoice date
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_simplified_invoices_tenant ON purser.simplified_invoices(tenant_id);
CREATE INDEX IF NOT EXISTS idx_simplified_invoices_issued ON purser.simplified_invoices(issued_at);
CREATE INDEX IF NOT EXISTS idx_simplified_invoices_reference ON purser.simplified_invoices(reference_type, reference_id);

-- ============================================================================
-- TENANT BALANCE ROLLUPS (STATISTICS)
-- ============================================================================
-- Pre-aggregated lifetime stats per tenant for instant queries.
-- Updated atomically on each balance transaction.
-- ============================================================================

CREATE TABLE IF NOT EXISTS purser.tenant_balance_rollups (
    tenant_id UUID PRIMARY KEY,

    -- Lifetime totals
    total_topup_cents BIGINT NOT NULL DEFAULT 0,       -- All-time top-ups (original currency)
    total_topup_eur_cents BIGINT NOT NULL DEFAULT 0,   -- All-time top-ups (EUR equivalent)
    total_usage_cents BIGINT NOT NULL DEFAULT 0,       -- All-time usage deductions

    -- Counts
    topup_count INTEGER NOT NULL DEFAULT 0,

    -- Timestamps
    first_topup_at TIMESTAMPTZ,
    last_topup_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenant_balance_rollups_last_topup ON purser.tenant_balance_rollups(last_topup_at);
