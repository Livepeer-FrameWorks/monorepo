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
    -- manual_review is a hard hold: no payment, no Stripe meter push, no
    -- operator credit ledger insertion, no period advance until ops resolves
    -- and re-finalizes.
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
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
    usage_details JSONB NOT NULL DEFAULT '{}',       -- Raw usage breakdown for debug/audit; presentation surfaces (email, dashboard) read invoice_line_items.

    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),

    CONSTRAINT chk_billing_invoices_status CHECK (status IN (
        'draft', 'pending', 'paid', 'overdue', 'failed', 'cancelled', 'manual_review'
    ))
);

-- Payment transactions against invoices
CREATE TABLE IF NOT EXISTS purser.billing_payments (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    
    -- ===== PAYMENT DETAILS =====
    method VARCHAR(50) NOT NULL, -- card, crypto_eth, crypto_usdc, bank_transfer
    amount DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    tx_id VARCHAR(255), -- External transaction ID
    payment_url TEXT,
    actual_tx_amount DECIMAL(30,18),
    asset_type VARCHAR(10),
    network VARCHAR(20),
    block_number BIGINT,
    
    -- ===== STATUS =====
    status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, confirmed, failed
    confirmed_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT chk_billing_payments_method CHECK (method IN ('card', 'crypto_eth', 'crypto_usdc', 'bank_transfer')),
    
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

    -- For prepaid top-ups: expected amount in the tenant's prepaid currency
    -- (EUR or USD cents). NULL for invoice — amount comes from the invoice.
    expected_amount_cents BIGINT,

    -- ===== WALLET DETAILS =====
    -- Asset type: ETH (native), USDC (ERC-20), LPT (ERC-20)
    asset VARCHAR(10) NOT NULL,
    CONSTRAINT chk_wallet_asset CHECK (asset IN ('ETH', 'USDC', 'LPT')),

    -- Network: must be persisted explicitly by the writer (no default — the
    -- caller picks per asset / request).
    network VARCHAR(20) NOT NULL,
    CONSTRAINT chk_wallet_network CHECK (network IN ('ethereum', 'base', 'arbitrum', 'base-sepolia', 'arbitrum-sepolia')),

    wallet_address VARCHAR(255) NOT NULL,

    -- HD wallet derivation index (from xpub) - enables address regeneration
    derivation_index INTEGER NOT NULL,

    -- ===== STATUS & LIFECYCLE =====
    -- pending:    address issued, no on-chain payment seen
    -- confirming: payment detected, awaiting required confirmations
    -- completed:  confirmations met, balance credited (sweepable)
    -- swept:      funds moved to cold storage
    -- expired:    no payment received before expires_at
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    CONSTRAINT chk_wallet_status CHECK (status IN ('pending', 'confirming', 'completed', 'swept', 'expired')),

    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,

    -- ===== QUOTE (locked at CreateCryptoTopup time) =====
    -- For non-USDC assets: USD price per whole token, read from a Chainlink
    -- aggregator and persisted so the credit at confirm time uses the same
    -- price the user was quoted. For USDC: 1.0 with quote_source='one_to_one'.
    expected_amount_base_units NUMERIC(78,0),  -- token base units (wei for 18-dec, 1e6 for USDC)
    quoted_price_usd           NUMERIC(28,18), -- USD per 1 whole token
    quoted_usd_to_eur_rate     NUMERIC(12,8),  -- usd_cents * rate = eur_cents (set when currency=EUR)
    quoted_at                  TIMESTAMP WITH TIME ZONE,
    quote_source               VARCHAR(20),    -- 'chainlink' | 'one_to_one'

    -- ===== ON-CHAIN RESULT =====
    tx_hash                    VARCHAR(66),
    block_number               BIGINT,
    confirmations              INTEGER NOT NULL DEFAULT 0,
    received_amount_base_units NUMERIC(78,0),
    detected_at                TIMESTAMP WITH TIME ZONE,
    completed_at               TIMESTAMP WITH TIME ZONE,

    -- ===== CREDIT =====
    -- Amount credited to the tenant's prepaid balance, in that balance's
    -- currency. For USD-denominated balances this is USD cents; for EUR,
    -- the converted EUR cents using quoted_usd_to_eur_rate.
    credited_amount_cents      BIGINT,
    credited_amount_currency   VARCHAR(3),

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
    CONSTRAINT chk_wallet_quote_source CHECK (
        quote_source IS NULL OR quote_source IN ('chainlink', 'one_to_one')
    ),
    CONSTRAINT chk_wallet_credited_currency CHECK (
        credited_amount_currency IS NULL OR credited_amount_currency ~ '^[A-Z]{3}$'
    ),

    -- Unique constraints:
    -- Prepaid and invoice wallets share one global derivation index pool.
    UNIQUE(derivation_index)
);

CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_active ON purser.crypto_wallets(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_tenant ON purser.crypto_wallets(tenant_id, purpose);
CREATE INDEX IF NOT EXISTS idx_purser_crypto_wallets_address ON purser.crypto_wallets(wallet_address);
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_crypto_wallets_active_invoice_asset
    ON purser.crypto_wallets(invoice_id, asset)
    WHERE purpose = 'invoice' AND status IN ('pending', 'confirming');
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_crypto_wallets_tx
    ON purser.crypto_wallets(network, tx_hash)
    WHERE tx_hash IS NOT NULL;

-- ============================================================================
-- BILLING TIERS & SUBSCRIPTION PLANS
-- ============================================================================

-- Service tier definitions. Metered pricing and non-billing entitlements live in
-- tier_pricing_rules and tier_entitlements.
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
    
    -- ===== FEATURES & SUPPORT =====
    -- Pricing rules live in purser.tier_pricing_rules; entitlements (e.g.
    -- recording_retention_days) live in purser.tier_entitlements.
    features JSONB NOT NULL DEFAULT '{}',    -- Feature flags and capabilities
    support_level VARCHAR(50) DEFAULT 'community',
    sla_level VARCHAR(50) DEFAULT 'none',

    -- ===== METERING =====
    metering_enabled BOOLEAN DEFAULT false,


    -- ===== STATUS & TIER LEVEL =====
    is_active BOOLEAN DEFAULT true,
    tier_level INTEGER DEFAULT 0,
    is_enterprise BOOLEAN DEFAULT false,

    -- ===== STRIPE INTEGRATION =====
    stripe_price_id_monthly VARCHAR(255),
    stripe_price_id_yearly VARCHAR(255),
    stripe_product_id VARCHAR(255),

    -- ===== DEFAULT TIER FLAGS =====
    is_default_prepaid BOOLEAN DEFAULT false,
    is_default_postpaid BOOLEAN DEFAULT false,

    -- ===== MISTSERVER PROCESS DEFINITIONS =====
    -- Raw JSON arrays of MistServer process objects per stream type.
    -- Use {{gateway_url}} placeholder for Livepeer broadcaster address.
    processes_live JSONB DEFAULT '[]',
    processes_vod JSONB DEFAULT '[]',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- TIER ENTITLEMENTS & PRICING RULES
-- ============================================================================
-- The rating engine in api_billing/internal/rating consumes pricing rules to
-- turn metered usage into invoice line items. Entitlements are non-billing
-- grants (e.g. recording_retention_days drives Foghorn lifecycle, not money).

-- Non-pricing grants attached to a tier. Values are JSON-encoded scalars.
-- Canonical shape: the bare YAML scalar (e.g. value=90, value="basic"). The
-- bootstrap reconciler, migration backfill, and parseRetentionDays all agree
-- on this; do not introduce wrapper objects.
CREATE TABLE IF NOT EXISTS purser.tier_entitlements (
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id) ON DELETE CASCADE,
    key VARCHAR(64) NOT NULL,
    value JSONB NOT NULL,
    PRIMARY KEY (tier_id, key)
);

-- One row per (tier, meter). model is one of:
--   tiered_graduated  -- (qty - included_quantity) * unit_price
--   all_usage         -- qty * unit_price
--   codec_multiplier  -- per-codec processing fee using config.codec_multipliers
CREATE TABLE IF NOT EXISTS purser.tier_pricing_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id) ON DELETE CASCADE,
    meter VARCHAR(64) NOT NULL,
    model VARCHAR(32) NOT NULL,
    currency CHAR(3) NOT NULL,
    included_quantity NUMERIC(20,6) NOT NULL DEFAULT 0,
    unit_price NUMERIC(20,9) NOT NULL DEFAULT 0,
    config JSONB NOT NULL DEFAULT '{}',
    CONSTRAINT chk_tier_pricing_meter CHECK (meter IN ('delivered_minutes', 'average_storage_gb', 'ai_gpu_hours', 'processing_seconds')),
    CONSTRAINT chk_tier_pricing_model CHECK (model IN ('tiered_graduated', 'all_usage', 'codec_multiplier')),
    UNIQUE (tier_id, meter)
);

CREATE INDEX IF NOT EXISTS idx_tier_pricing_rules_tier ON purser.tier_pricing_rules(tier_id);

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
    -- Per-tenant pricing/entitlement overrides live in
    -- purser.subscription_pricing_overrides and
    -- purser.subscription_entitlement_overrides.
    custom_features JSONB DEFAULT '{}',      -- Custom feature flags

    -- ===== PAYMENT & BILLING =====
    payment_method VARCHAR(50),
    payment_reference VARCHAR(255),
    billing_company VARCHAR(255),         -- Company name for invoices
    billing_address JSONB,                -- Structured: {line1, line2, city, postalCode, country}
    tax_id VARCHAR(100),                  -- VAT number (EU format: XX123456789)
    tax_rate DECIMAL(5,4) DEFAULT 0.0000,

    -- ===== STRIPE INTEGRATION =====
    stripe_customer_id VARCHAR(255),
    stripe_subscription_id VARCHAR(255),
    stripe_subscription_status VARCHAR(50),
    stripe_current_period_end TIMESTAMPTZ,
    dunning_attempts INT DEFAULT 0,

    -- ===== MOLLIE INTEGRATION =====
    mollie_subscription_id VARCHAR(50),

    -- ===== X402 PROTOCOL =====
    x402_address_index INTEGER,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT chk_billing_model CHECK (billing_model IN ('postpaid', 'prepaid')),
    UNIQUE(tenant_id)
);

-- ============================================================================
-- SUBSCRIPTION OVERRIDES
-- ============================================================================
-- Per-subscription overlays on top of the tier catalog. The rating engine
-- merges tier_pricing_rules + subscription_pricing_overrides at rate time;
-- entitlement overrides shadow tier_entitlements similarly.

CREATE TABLE IF NOT EXISTS purser.subscription_pricing_overrides (
    subscription_id UUID NOT NULL REFERENCES purser.tenant_subscriptions(id) ON DELETE CASCADE,
    meter VARCHAR(64) NOT NULL,
    model VARCHAR(32),
    currency CHAR(3),
    included_quantity NUMERIC(20,6),
    unit_price NUMERIC(20,9),
    config JSONB DEFAULT '{}',
    CONSTRAINT chk_subscription_pricing_meter CHECK (meter IN ('delivered_minutes', 'average_storage_gb', 'ai_gpu_hours', 'processing_seconds')),
    CONSTRAINT chk_subscription_pricing_model CHECK (model IS NULL OR model IN ('tiered_graduated', 'all_usage', 'codec_multiplier')),
    PRIMARY KEY (subscription_id, meter)
);

CREATE TABLE IF NOT EXISTS purser.subscription_entitlement_overrides (
    subscription_id UUID NOT NULL REFERENCES purser.tenant_subscriptions(id) ON DELETE CASCADE,
    key VARCHAR(64) NOT NULL,
    value JSONB NOT NULL,
    PRIMARY KEY (subscription_id, key)
);

-- ============================================================================
-- INVOICE LINE ITEMS
-- ============================================================================
-- One row per priced behavior on an invoice. Generated by rating.Rate and
-- upserted on (invoice_id, line_key). Drafts refresh as usage accumulates;
-- finalized invoices are application-layer-guarded against further writes.

CREATE TABLE IF NOT EXISTS purser.invoice_line_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    -- Denormalized tenant_id so financial-audit reads can filter by tenant
    -- without a join. Required by the cross-service tenant-filter rule.
    tenant_id UUID NOT NULL,
    line_key VARCHAR(128) NOT NULL,
    meter VARCHAR(64),
    description TEXT NOT NULL,
    quantity NUMERIC(20,6) NOT NULL,
    included_quantity NUMERIC(20,6) NOT NULL DEFAULT 0,
    billable_quantity NUMERIC(20,6) NOT NULL,
    unit_price NUMERIC(20,9) NOT NULL,
    amount NUMERIC(20,2) NOT NULL,
    currency CHAR(3) NOT NULL,
    -- Cluster attribution: NULL for tenant-scoped lines (base_subscription).
    -- Set for cluster usage lines.
    cluster_id VARCHAR(100),
    cluster_kind VARCHAR(32),
    cluster_owner_tenant_id UUID,
    -- Why the line was priced the way it was. Stamped at rating time.
    pricing_source VARCHAR(32) NOT NULL DEFAULT 'tier',
    -- Per-line operator credit and platform fee. Non-zero only for
    -- third_party_marketplace lines.
    operator_credit_cents BIGINT NOT NULL DEFAULT 0,
    platform_fee_cents BIGINT NOT NULL DEFAULT 0,
    -- Snapshot of cluster_pricing_history.version_id at rating time, so
    -- a later mid-period repricing remains auditable per-line.
    price_version_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (invoice_id, line_key),
    CONSTRAINT chk_invoice_line_items_pricing_source CHECK (pricing_source IN (
        'tier', 'cluster_metered', 'cluster_monthly', 'cluster_custom',
        'free_unmetered', 'self_hosted', 'included_subscription'
    )),
    CONSTRAINT chk_invoice_line_items_cluster_kind CHECK (cluster_kind IS NULL OR cluster_kind IN (
        'platform_official', 'tenant_private', 'third_party_marketplace'
    ))
);

CREATE INDEX IF NOT EXISTS idx_invoice_line_items_invoice ON purser.invoice_line_items(invoice_id);
CREATE INDEX IF NOT EXISTS idx_invoice_line_items_tenant ON purser.invoice_line_items(tenant_id);
CREATE INDEX IF NOT EXISTS idx_invoice_line_items_cluster ON purser.invoice_line_items(cluster_id) WHERE cluster_id IS NOT NULL;

-- Operator credit ledger: one accrual per priced third_party_marketplace
-- line; clawbacks/adjustments reference the original via reverses_ledger_id.
-- payable_cents is signed so SUM aggregates net.
-- A ledger row is sourced either from a usage-priced invoice line
-- (source_type='invoice_line', invoice_line_item_id set) or a Stripe
-- subscription invoice for a monthly cluster (source_type='stripe_subscription',
-- stripe_invoice_id set). The split exists because monthly cluster revenue
-- is collected entirely on the Stripe side and never produces a row in
-- purser.invoice_line_items.
--
-- Default status is 'held' so credits accumulate as a complete audit
-- trail without auto-promoting to payable. Operator vetting flips the
-- relevant rows to 'accruing' (or the writer can default to 'accruing'
-- when cluster_owners.status='approved' AND payout_eligible).
CREATE TABLE IF NOT EXISTS purser.operator_credit_ledger (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_type VARCHAR(32) NOT NULL DEFAULT 'invoice_line',
    invoice_line_item_id UUID REFERENCES purser.invoice_line_items(id) ON DELETE CASCADE,
    stripe_invoice_id VARCHAR(255),
    entry_type VARCHAR(16) NOT NULL,
    reverses_ledger_id UUID REFERENCES purser.operator_credit_ledger(id),
    cluster_owner_tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    currency CHAR(3) NOT NULL,
    gross_cents BIGINT NOT NULL DEFAULT 0,
    platform_fee_cents BIGINT NOT NULL DEFAULT 0,
    payable_cents BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'held',
    payout_batch_id UUID,
    notes JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_op_credit_entry_type CHECK (entry_type IN ('accrual', 'clawback', 'adjustment')),
    CONSTRAINT chk_op_credit_status CHECK (status IN ('accruing', 'eligible', 'paid_out', 'clawed_back', 'held')),
    CONSTRAINT chk_op_credit_source CHECK (
        (source_type = 'invoice_line'        AND invoice_line_item_id IS NOT NULL) OR
        (source_type = 'stripe_subscription' AND stripe_invoice_id    IS NOT NULL)
    ),
    CONSTRAINT chk_op_credit_reverses CHECK (
        (entry_type = 'accrual' AND reverses_ledger_id IS NULL) OR
        (entry_type IN ('clawback', 'adjustment') AND reverses_ledger_id IS NOT NULL)
    )
);

-- One accrual per priced invoice line.
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_invoice_line
    ON purser.operator_credit_ledger(invoice_line_item_id)
    WHERE entry_type = 'accrual' AND source_type = 'invoice_line';
-- One accrual per Stripe subscription invoice (Stripe enforces uniqueness on
-- invoice_id; this index makes our writer idempotent on retry too).
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_stripe_invoice
    ON purser.operator_credit_ledger(stripe_invoice_id)
    WHERE entry_type = 'accrual' AND source_type = 'stripe_subscription';

CREATE INDEX IF NOT EXISTS idx_op_credit_owner_status
    ON purser.operator_credit_ledger(cluster_owner_tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_op_credit_invoice
    ON purser.operator_credit_ledger(invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_op_credit_payout_batch
    ON purser.operator_credit_ledger(payout_batch_id) WHERE payout_batch_id IS NOT NULL;

-- Per-tenant operator vetting / KYC / payout-eligibility state. Keyed by
-- the owning tenant_id (Quartermaster's infrastructure_clusters.owner_tenant_id
-- joins here). Marketplace listings and CreateClusterSubscription gate on
-- status='approved' AND payout_eligible once marketplace launches; until
-- then the table is populated by ops via internal tooling and consulted by
-- the credit ledger writer to set ledger status.
CREATE TABLE IF NOT EXISTS purser.cluster_owners (
    tenant_id UUID PRIMARY KEY,
    -- draft: row exists, KYC not started
    -- pending_review: KYC submitted, awaiting ops decision
    -- approved: vetted; marketplace clusters can be public + payouts eligible
    -- suspended: paused (TOS violation, payout dispute, etc.)
    status VARCHAR(32) NOT NULL DEFAULT 'draft',
    payout_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    legal_entity_name VARCHAR(255),
    contact_email VARCHAR(255),
    -- ISO 3166-1 alpha-2 country code for tax routing.
    tax_country CHAR(2),
    tax_id VARCHAR(64),
    -- Free-form metadata for the KYC provider (Stripe Connect account id,
    -- Persona inquiry id, etc.). Schema-less by design — the provider
    -- contract dictates the fields.
    kyc_metadata JSONB NOT NULL DEFAULT '{}',
    payout_method VARCHAR(32),                -- bank_transfer | stripe_connect | manual | ...
    payout_metadata JSONB NOT NULL DEFAULT '{}',
    notes TEXT,
    approved_at TIMESTAMPTZ,
    approved_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_cluster_owners_status CHECK (status IN ('draft', 'pending_review', 'approved', 'suspended'))
);

CREATE INDEX IF NOT EXISTS idx_cluster_owners_status
    ON purser.cluster_owners(status);
CREATE INDEX IF NOT EXISTS idx_cluster_owners_payout_eligible
    ON purser.cluster_owners(tenant_id) WHERE payout_eligible = TRUE;

-- Settlement skeleton. Real integration (bank transfer, Stripe Connect)
-- happens elsewhere; this table exists so payout_batch_id has a destination
-- and so reads can JOIN.
CREATE TABLE IF NOT EXISTS purser.operator_payouts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_owner_tenant_id UUID NOT NULL,
    currency CHAR(3) NOT NULL,
    total_cents BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    method VARCHAR(32),
    external_reference VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at TIMESTAMPTZ,
    CONSTRAINT chk_op_payout_status CHECK (status IN ('pending', 'processing', 'paid', 'failed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_op_payouts_owner_status
    ON purser.operator_payouts(cluster_owner_tenant_id, status);

-- Platform fee policy: effective-dated, optionally per-owner. NULL
-- cluster_owner_tenant_id is the global default for the kind.
CREATE TABLE IF NOT EXISTS purser.platform_fee_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_kind VARCHAR(32) NOT NULL,
    cluster_owner_tenant_id UUID,
    pricing_source VARCHAR(32),
    fee_basis_points INT NOT NULL DEFAULT 0,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    effective_to TIMESTAMPTZ,
    notes TEXT,
    CONSTRAINT chk_platform_fee_kind CHECK (cluster_kind IN (
        'platform_official', 'tenant_private', 'third_party_marketplace'
    )),
    CONSTRAINT chk_platform_fee_window CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT chk_platform_fee_bps CHECK (fee_basis_points >= 0 AND fee_basis_points <= 10000)
);

CREATE INDEX IF NOT EXISTS idx_platform_fee_lookup
    ON purser.platform_fee_policy(cluster_kind, cluster_owner_tenant_id, effective_from DESC)
    WHERE effective_to IS NULL;

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
    -- Sub-cent residual from rated micro-events. Each prepaid deduction
    -- accumulates fractional cents here until they cross a whole-cent boundary,
    -- so per-event usage under €0.01 doesn't structurally leak revenue.
    -- Unit is millionths of a cent (10^-8 of a unit currency).
    balance_remainder_micro BIGINT NOT NULL DEFAULT 0,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',

    -- ===== ALERTS =====
    low_balance_threshold_cents BIGINT DEFAULT 500,   -- Alert when below €5

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
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',

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
    usage_type VARCHAR(50) NOT NULL,         -- viewer_hours, average_storage_gb, gpu_hours, codec seconds, operational metrics
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_invoices_tenant_period_unique
    ON purser.billing_invoices(tenant_id, period_start)
    WHERE period_start IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_purser_billing_payments_invoice_id ON purser.billing_payments(invoice_id);
CREATE INDEX IF NOT EXISTS idx_purser_billing_payments_tx_id ON purser.billing_payments(tx_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_payments_tx_unique
    ON purser.billing_payments(tx_id)
    WHERE tx_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_payments_pending_invoice_method
    ON purser.billing_payments(invoice_id, method)
    WHERE status = 'pending';

-- ============================================================================
-- BILLING & SUBSCRIPTION INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_active ON purser.billing_tiers(is_active, tier_level);
CREATE INDEX IF NOT EXISTS idx_purser_billing_tiers_enterprise ON purser.billing_tiers(is_enterprise);
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_tiers_default_prepaid ON purser.billing_tiers((1)) WHERE is_default_prepaid = true;
CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_tiers_default_postpaid ON purser.billing_tiers((1)) WHERE is_default_postpaid = true;
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
    -- Stripe meter event_name (NOT the mtr_xxx ID). This is the value
    -- passed as BillingMeterEventParams.EventName when firing meter
    -- events. The Stripe Billing Meter has both an id (mtr_xxx) and an
    -- event_name (the customer-facing string); usage events route by
    -- event_name. Setting the wrong value here drops the events on the
    -- floor.
    stripe_meter_event_name VARCHAR(255),

    -- ===== BASE PRICING (for 'monthly' model) =====
    base_price DECIMAL(10,2) DEFAULT 0.00,
    currency VARCHAR(3) DEFAULT 'EUR',

    -- ===== METERED RATES (override tenant tier rates) =====
    -- Format: per-meter object with unit_price (required) and optional
    -- model (defaults to all_usage), included_quantity, config:
    --   {
    --     "delivered_minutes":  {"unit_price": "0.0005", "model": "tiered_graduated", "included_quantity": "0"},
    --     "average_storage_gb": {"unit_price": "0.01"}
    --   }
    -- Validated at write time by SetClusterPricing; runtime shape must
    -- match validateMeteredRatesShape in api_billing/internal/grpc.
    metered_rates JSONB DEFAULT '{}',

    -- ===== VISIBILITY & ACCESS RULES =====
    -- Minimum tier level required to see/subscribe to this cluster
    -- 0=no subscription required, 1=free, 2=supporter, 3=developer, 4=production, 5=enterprise
    required_tier_level INT DEFAULT 0,
    -- is_platform_official moved to Quartermaster (infrastructure_clusters.is_platform_official)
    allow_free_tier BOOLEAN DEFAULT FALSE,       -- If platform_official, allow free tier access

    -- ===== DEFAULT QUOTAS (for free_unmetered or as caps) =====
    -- Format: {"max_streams": 2, "max_viewers": 50, "max_bandwidth_mbps": 100, "retention_days": 7}
    default_quotas JSONB DEFAULT '{}',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

    CONSTRAINT chk_cluster_pricing_model CHECK (pricing_model IN ('free_unmetered', 'metered', 'monthly', 'tier_inherit', 'custom'))
);

CREATE INDEX IF NOT EXISTS idx_purser_cluster_pricing_model ON purser.cluster_pricing(pricing_model);
CREATE INDEX IF NOT EXISTS idx_purser_cluster_pricing_tier_level ON purser.cluster_pricing(required_tier_level);
-- A Stripe meter is owned by exactly one cluster; collisions would mis-route
-- usage events. Partial index permits NULLs (clusters with no metered model).
CREATE UNIQUE INDEX IF NOT EXISTS idx_cluster_pricing_stripe_meter
    ON purser.cluster_pricing(stripe_meter_event_name)
    WHERE stripe_meter_event_name IS NOT NULL;

-- At-least-once delivery surface for Stripe meter events. Rows are written
-- inside the invoice finalization tx; an async flusher reads pending rows
-- (sent_at IS NULL) and pushes to Stripe. UNIQUE (tenant, cluster, meter,
-- period) makes re-running finalization a no-op.
CREATE TABLE IF NOT EXISTS purser.stripe_meter_events_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    meter VARCHAR(64) NOT NULL,
    stripe_meter_event_name VARCHAR(255) NOT NULL,
    quantity NUMERIC(20,6) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE SET NULL,
    sent_at TIMESTAMPTZ,
    attempt_count INT NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_stripe_meter_events_outbox_period
    ON purser.stripe_meter_events_outbox(tenant_id, cluster_id, meter, stripe_meter_event_name, period_start);
CREATE INDEX IF NOT EXISTS idx_stripe_meter_events_outbox_pending
    ON purser.stripe_meter_events_outbox(created_at)
    WHERE sent_at IS NULL;

-- Effective-dated audit trail of cluster pricing config. Rating reads the row
-- effective at period_start so a mid-period repricing remains visible
-- per-version on the invoice. The trigger below maintains the timeline.
CREATE TABLE IF NOT EXISTS purser.cluster_pricing_history (
    version_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id VARCHAR(100) NOT NULL,
    pricing_model VARCHAR(20) NOT NULL,
    stripe_product_id VARCHAR(255),
    stripe_price_id_monthly VARCHAR(255),
    stripe_meter_event_name VARCHAR(255),
    base_price DECIMAL(10,2) NOT NULL DEFAULT 0,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    metered_rates JSONB NOT NULL DEFAULT '{}',
    required_tier_level INT NOT NULL DEFAULT 0,
    allow_free_tier BOOLEAN NOT NULL DEFAULT FALSE,
    default_quotas JSONB NOT NULL DEFAULT '{}',
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_cluster_pricing_history_model
        CHECK (pricing_model IN ('free_unmetered', 'metered', 'monthly', 'tier_inherit', 'custom')),
    CONSTRAINT chk_cluster_pricing_history_window
        CHECK (effective_to IS NULL OR effective_to > effective_from)
);

CREATE INDEX IF NOT EXISTS idx_cluster_pricing_history_cluster_open
    ON purser.cluster_pricing_history(cluster_id)
    WHERE effective_to IS NULL;
CREATE INDEX IF NOT EXISTS idx_cluster_pricing_history_cluster_window
    ON purser.cluster_pricing_history(cluster_id, effective_from DESC);

CREATE OR REPLACE FUNCTION purser.cluster_pricing_history_record()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE purser.cluster_pricing_history
        SET effective_to = NOW()
        WHERE cluster_id = NEW.cluster_id
          AND effective_to IS NULL;
    INSERT INTO purser.cluster_pricing_history (
        cluster_id, pricing_model,
        stripe_product_id, stripe_price_id_monthly, stripe_meter_event_name,
        base_price, currency, metered_rates,
        required_tier_level, allow_free_tier, default_quotas,
        effective_from
    ) VALUES (
        NEW.cluster_id, NEW.pricing_model,
        NEW.stripe_product_id, NEW.stripe_price_id_monthly, NEW.stripe_meter_event_name,
        NEW.base_price, NEW.currency, NEW.metered_rates,
        NEW.required_tier_level, NEW.allow_free_tier, NEW.default_quotas,
        NOW()
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_cluster_pricing_history ON purser.cluster_pricing;
CREATE TRIGGER trg_cluster_pricing_history
    AFTER INSERT OR UPDATE ON purser.cluster_pricing
    FOR EACH ROW EXECUTE FUNCTION purser.cluster_pricing_history_record();

CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_stripe_customer ON purser.tenant_subscriptions(stripe_customer_id);
CREATE INDEX IF NOT EXISTS idx_purser_tenant_subscriptions_stripe_subscription ON purser.tenant_subscriptions(stripe_subscription_id);

-- ============================================================================
-- MOLLIE PAYMENT INFRASTRUCTURE
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

-- ============================================================================
-- MIGRATIONS (idempotent column additions for existing databases)
-- ============================================================================

ALTER TABLE purser.billing_tiers
  ADD COLUMN IF NOT EXISTS processes_live JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_vod JSONB DEFAULT '[]';
