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

CREATE SEQUENCE IF NOT EXISTS purser.billing_invoice_number_seq;

-- Invoice generation and payment tracking
CREATE TABLE IF NOT EXISTS purser.billing_invoices (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    invoice_number VARCHAR(50) NOT NULL DEFAULT (
        'INV-' || LPAD(nextval('purser.billing_invoice_number_seq')::text, 10, '0')
    ),

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
    metered_amount DECIMAL(10,2) NOT NULL DEFAULT 0, -- Usage-based charges (net; 0 when usage is waived)
    -- Unwaived metered total: what usage would have rated to. Equals metered_amount
    -- when WAIVE_USAGE_CHARGES is off; the would-have-cost figure when on. Display
    -- only, never charged. Wider precision than other money columns so a metering
    -- bug producing garbage usage cannot overflow it and abort invoice generation.
    gross_metered_amount DECIMAL(20,2) NOT NULL DEFAULT 0,
    prepaid_credit_applied DECIMAL(10,2) NOT NULL DEFAULT 0, -- Credit applied from prepaid balance
    usage_details JSONB NOT NULL DEFAULT '{}',       -- Raw usage breakdown for debug/audit; presentation surfaces (email, dashboard) read invoice_line_items.

    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years'),

    CONSTRAINT chk_billing_invoices_status CHECK (status IN (
        'draft', 'pending', 'paid', 'overdue', 'failed', 'cancelled', 'manual_review'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_invoices_number
    ON purser.billing_invoices(invoice_number);

-- Small provider-collected postpaid balances are retained until the economic
-- collection floor is reached. Entries provide per-invoice reconciliation;
-- the balance row is the lockable current state.
CREATE TABLE IF NOT EXISTS purser.billing_collection_balances (
    tenant_id UUID NOT NULL,
    currency CHAR(3) NOT NULL,
    balance_cents BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, currency),
    CONSTRAINT chk_billing_collection_balance_nonnegative CHECK (balance_cents >= 0)
);

CREATE TABLE IF NOT EXISTS purser.billing_collection_entries (
    invoice_id UUID PRIMARY KEY REFERENCES purser.billing_invoices(id) ON DELETE RESTRICT,
    tenant_id UUID NOT NULL,
    provider VARCHAR(32) NOT NULL,
    currency CHAR(3) NOT NULL,
    minimum_cents BIGINT NOT NULL,
    opening_balance_cents BIGINT NOT NULL,
    current_charge_cents BIGINT NOT NULL,
    collected_cents BIGINT NOT NULL,
    closing_balance_cents BIGINT NOT NULL,
    outcome VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_billing_collection_minimum_positive CHECK (minimum_cents > 0),
    CONSTRAINT chk_billing_collection_entry_amounts CHECK (
        opening_balance_cents >= 0 AND current_charge_cents >= 0 AND
        collected_cents >= 0 AND closing_balance_cents >= 0
    ),
    CONSTRAINT chk_billing_collection_entry_outcome CHECK (outcome IN ('deferred', 'collected', 'none')),
    CONSTRAINT chk_billing_collection_entry_math CHECK (
        opening_balance_cents + current_charge_cents = collected_cents + closing_balance_cents
    )
);

CREATE INDEX IF NOT EXISTS idx_billing_collection_entries_tenant_created
    ON purser.billing_collection_entries(tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS purser.billing_collection_writeoffs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    currency CHAR(3) NOT NULL,
    amount_cents BIGINT NOT NULL,
    reason VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_billing_collection_writeoff_positive CHECK (amount_cents > 0),
    CONSTRAINT chk_billing_collection_writeoff_reason CHECK (reason IN ('account_closed', 'operator_approved'))
);

CREATE INDEX IF NOT EXISTS idx_billing_collection_writeoffs_tenant_created
    ON purser.billing_collection_writeoffs(tenant_id, created_at DESC);

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
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years')
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
	derivation_xpub TEXT NOT NULL,

    -- ===== STATUS & LIFECYCLE =====
    -- pending:    address issued, no on-chain payment seen
    -- confirming: payment detected, awaiting required confirmations
    -- review_required: confirmed funds need explicit operator allocation
    -- completed:  confirmations met, balance credited (sweepable)
    -- swept:      funds moved to cold storage
    -- expired:    no payment received before expires_at
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    CONSTRAINT chk_wallet_status CHECK (status IN ('pending', 'confirming', 'review_required', 'completed', 'swept', 'expired')),

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
    client_ip                  VARCHAR(64),       -- Trusted request-time tax-location evidence
	tax_document_kind          VARCHAR(16) NOT NULL DEFAULT 'simplified' CHECK (tax_document_kind IN ('simplified', 'full')),
	tax_profile_snapshot       JSONB NOT NULL DEFAULT '{}'::jsonb,

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
	UNIQUE(derivation_xpub, derivation_index)
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

-- Restart-safe JSON-RPC observation. Cursor advancement and event inserts are
-- committed together; block hashes permit deterministic rewind on reorg.
CREATE TABLE IF NOT EXISTS purser.crypto_scan_cursors (
    network VARCHAR(20) PRIMARY KEY,
    next_block BIGINT NOT NULL CHECK (next_block >= 0),
    last_scanned_block BIGINT,
    last_scanned_block_hash VARCHAR(66),
    safe_head_block BIGINT,
    lag_blocks BIGINT,
    last_error TEXT,
    error_count INTEGER NOT NULL DEFAULT 0,
    scanned_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_deposit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    tx_hash VARCHAR(66) NOT NULL,
    log_index BIGINT NOT NULL DEFAULT -1,
    block_number BIGINT NOT NULL,
    block_hash VARCHAR(66) NOT NULL,
    from_address VARCHAR(42),
    to_address VARCHAR(42) NOT NULL,
    amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
    canonical BOOLEAN NOT NULL DEFAULT TRUE,
    status VARCHAR(24) NOT NULL DEFAULT 'observed' CHECK (
        status IN ('observed', 'confirmed', 'allocated', 'review_required', 'reorged')
    ),
    wallet_id UUID REFERENCES purser.crypto_wallets(id),
    confirmations INTEGER NOT NULL DEFAULT 0,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at TIMESTAMPTZ,
    allocated_at TIMESTAMPTZ,
    reorged_at TIMESTAMPTZ,
    last_canonical_checked_at TIMESTAMPTZ,
    allocation_error TEXT,
    UNIQUE(network, tx_hash, log_index)
);

CREATE INDEX IF NOT EXISTS idx_crypto_deposit_events_destination
    ON purser.crypto_deposit_events(network, to_address, status);
CREATE INDEX IF NOT EXISTS idx_crypto_deposit_events_allocation
    ON purser.crypto_deposit_events(status, block_number)
    WHERE canonical AND status IN ('observed', 'confirmed', 'review_required');

CREATE TABLE IF NOT EXISTS purser.crypto_custody_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    source_kind VARCHAR(20) NOT NULL CHECK (source_kind IN ('direct_deposit', 'x402')),
    source_ref UUID,
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    address VARCHAR(42) NOT NULL,
    derivation_index INTEGER NOT NULL,
	derivation_xpub TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(network, asset, address),
    UNIQUE(source_kind, source_ref, network, asset)
);

-- Manual sweep ceremony inventory. The online service never stores a seed,
-- xprv, child key, or unencrypted signing material.
CREATE TABLE IF NOT EXISTS purser.crypto_sweep_batches (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_version INTEGER NOT NULL,
    network VARCHAR(20) NOT NULL,
    treasury_address VARCHAR(42) NOT NULL,
    snapshot_block BIGINT NOT NULL,
    snapshot_block_hash VARCHAR(66) NOT NULL,
    manifest_checksum VARCHAR(64) NOT NULL UNIQUE,
    status VARCHAR(24) NOT NULL DEFAULT 'planned' CHECK (
	    status IN ('planned', 'signed', 'broadcast', 'partially_confirmed', 'confirmed', 'failed', 'expired', 'quarantined')
    ),
    expires_at TIMESTAMPTZ NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id UUID NOT NULL REFERENCES purser.crypto_sweep_batches(id),
    custody_address_id UUID NOT NULL REFERENCES purser.crypto_custody_addresses(id),
    wallet_id UUID REFERENCES purser.crypto_wallets(id),
    network VARCHAR(20) NOT NULL,
    asset VARCHAR(10) NOT NULL CHECK (asset IN ('ETH', 'USDC')),
    source_address VARCHAR(42) NOT NULL,
    derivation_index INTEGER NOT NULL,
    destination_address VARCHAR(42) NOT NULL,
    amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
    chain_id BIGINT NOT NULL,
    asset_contract VARCHAR(42),
    source_nonce BIGINT,
    max_fee_per_gas NUMERIC(78, 0),
    max_priority_fee_per_gas NUMERIC(78, 0),
	gas_limit BIGINT,
	authorization_nonce VARCHAR(66),
	authorization_after BIGINT,
	authorization_before BIGINT,
	signed_payload TEXT,
    relay_transaction TEXT,
    tx_hash VARCHAR(66),
    status VARCHAR(24) NOT NULL DEFAULT 'planned' CHECK (
	    status IN ('planned', 'signed', 'broadcast', 'confirmed', 'failed', 'expired', 'quarantined')
    ),
    failure_reason TEXT,
    broadcast_at TIMESTAMPTZ,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(batch_id, custody_address_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_crypto_sweep_items_tx
    ON purser.crypto_sweep_items(network, tx_hash)
    WHERE tx_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_sources (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	item_id UUID NOT NULL REFERENCES purser.crypto_sweep_items(id),
	source_type VARCHAR(20) NOT NULL CHECK (source_type IN ('direct_wallet', 'x402_quote')),
	source_id UUID NOT NULL,
	amount_base_units NUMERIC(78, 0) NOT NULL CHECK (amount_base_units > 0),
	claim_status VARCHAR(20) NOT NULL DEFAULT 'claimed' CHECK (
	    claim_status IN ('claimed', 'released', 'consumed', 'quarantined')
	),
	claimed_by UUID,
	claim_reason TEXT NOT NULL DEFAULT 'sweep plan',
	claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	released_by UUID,
	release_reason TEXT,
	released_at TIMESTAMPTZ,
	consumed_at TIMESTAMPTZ,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_crypto_sweep_sources_active
	ON purser.crypto_sweep_sources(source_type, source_id)
	WHERE claim_status IN ('claimed', 'consumed', 'quarantined');

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_events (
    id BIGSERIAL PRIMARY KEY,
    batch_id UUID NOT NULL REFERENCES purser.crypto_sweep_batches(id),
    item_id UUID REFERENCES purser.crypto_sweep_items(id),
    event_type VARCHAR(40) NOT NULL,
    actor_id UUID,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.crypto_sweep_relayer_nonces (
    network VARCHAR(20) PRIMARY KEY,
    next_nonce BIGINT NOT NULL CHECK (next_nonce >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
    -- Raw JSON arrays of MistServer process objects per lifecycle.
    -- Livepeer entries omit hardcoded_broadcasters; Foghorn fills the
    -- broadcaster list from its cluster's gateway instances at dispatch time.
    processes_live JSONB DEFAULT '[]',
    processes_dvr JSONB DEFAULT '[]',
    processes_clip JSONB DEFAULT '[]',
    processes_dvr_finalize JSONB DEFAULT '[]',
    processes_vod JSONB DEFAULT '[]',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- ============================================================================
-- TIER ENTITLEMENTS & PRICING RULES
-- ============================================================================
-- The rating engine in api_billing/internal/rating consumes pricing rules to
-- turn metered usage into invoice line items. Entitlements are non-billing
-- grants. recording_retention_days is the per-tier cap on customer-set
-- retention; the per-class system defaults and the resolution cascade live
-- in Commodore (see api_control/internal/grpc/media_retention.go).

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
--   dimensioned       -- per-bounded-dimension selector rates
CREATE TABLE IF NOT EXISTS purser.meter_definitions (
    meter VARCHAR(64) PRIMARY KEY,
    unit VARCHAR(32) NOT NULL,
    aggregation VARCHAR(16) NOT NULL DEFAULT 'sum',
    display_name VARCHAR(100) NOT NULL,
    allowed_dimensions TEXT[] NOT NULL DEFAULT '{}',
    default_priceable BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT chk_meter_definition_name CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT chk_meter_definition_aggregation CHECK (aggregation IN ('sum', 'max', 'last'))
);

INSERT INTO purser.meter_definitions
    (meter, unit, aggregation, display_name, allowed_dimensions, default_priceable)
VALUES
    ('delivered_minutes', 'minute', 'sum', 'Delivered minutes', '{}', TRUE),
    ('ingress_gb', 'gibibyte', 'sum', 'Ingress bandwidth', '{}', FALSE),
    ('egress_gb', 'gibibyte', 'sum', 'Egress bandwidth', '{}', TRUE),
    ('stream_runtime_seconds', 'second', 'sum', 'Stream runtime', '{}', FALSE),
    ('storage_gb_seconds_hot', 'gibibyte_second', 'sum', 'Hot storage', '{storage_backend,storage_scope}', FALSE),
    ('storage_gb_seconds_cold', 'gibibyte_second', 'sum', 'Cold storage', '{storage_backend,storage_scope}', TRUE),
    ('api_requests', 'request', 'sum', 'API requests', '{auth_type,operation_type,service}', FALSE),
    ('api_errors', 'request', 'sum', 'API errors', '{auth_type,operation_type,service}', FALSE),
    ('api_duration_ms', 'millisecond', 'sum', 'API duration', '{auth_type,operation_type,service}', FALSE),
    ('api_complexity', 'point', 'sum', 'API complexity', '{auth_type,operation_type,service}', FALSE),
    ('llm_input_tokens', 'token', 'sum', 'LLM input tokens', '{model,provider,service}', FALSE),
    ('llm_output_tokens', 'token', 'sum', 'LLM output tokens', '{model,provider,service}', FALSE),
    ('embedding_tokens', 'token', 'sum', 'Embedding tokens', '{model,service}', FALSE),
    ('embedding_requests', 'request', 'sum', 'Embedding requests', '{model,provider,service}', FALSE),
    ('search_requests', 'request', 'sum', 'Search requests', '{provider,service}', FALSE),
    ('transcode_input_seconds', 'second', 'sum', 'Transcode input', '{execution_backend,input_codec,output_codec,track_type,rendition_profile,resolution_class}', FALSE),
    ('transcode_rendition_seconds', 'second', 'sum', 'Transcode renditions', '{execution_backend,input_codec,output_codec,track_type,rendition_profile,resolution_class}', TRUE),
    ('remux_seconds', 'second', 'sum', 'Remux processing', '{execution_backend,output_codec,container}', FALSE),
    ('transcription_seconds', 'second', 'sum', 'Transcription', '{execution_backend,model,language}', FALSE),
    ('inference_frames', 'frame', 'sum', 'Inference frames', '{execution_backend,model}', FALSE),
    ('inference_input_tokens', 'token', 'sum', 'Inference input tokens', '{execution_backend,model}', FALSE),
    ('inference_output_tokens', 'token', 'sum', 'Inference output tokens', '{execution_backend,model}', FALSE),
    ('inference_invocations', 'invocation', 'sum', 'Inference invocations', '{execution_backend,model}', FALSE),
    ('peak_bandwidth_mbps', 'megabit_per_second', 'max', 'Peak bandwidth', '{}', FALSE),
    ('max_viewers', 'viewer', 'max', 'Peak viewers', '{}', FALSE),
    ('total_streams', 'stream', 'sum', 'Streams', '{}', FALSE),
    ('total_viewers', 'viewer', 'sum', 'Viewers', '{}', FALSE),
    ('unique_users', 'user', 'max', 'Unique users', '{}', FALSE),
    ('media_seconds', 'second', 'sum', 'Historical media processing', '{execution_backend,output_codec,track_type}', FALSE)
ON CONFLICT (meter) DO UPDATE SET
    unit = EXCLUDED.unit,
    aggregation = EXCLUDED.aggregation,
    display_name = EXCLUDED.display_name,
    allowed_dimensions = EXCLUDED.allowed_dimensions,
    default_priceable = EXCLUDED.default_priceable,
    active = TRUE;

CREATE TABLE IF NOT EXISTS purser.tier_pricing_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id) ON DELETE CASCADE,
    meter VARCHAR(64) NOT NULL,
    model VARCHAR(32) NOT NULL,
    currency CHAR(3) NOT NULL,
    included_quantity NUMERIC(20,6) NOT NULL DEFAULT 0,
    unit_price NUMERIC(20,9) NOT NULL DEFAULT 0,
    config JSONB NOT NULL DEFAULT '{}',
    CONSTRAINT chk_tier_pricing_meter CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT chk_tier_pricing_model CHECK (model IN ('tiered_graduated', 'all_usage', 'dimensioned')),
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

    -- ===== SCHEDULED TIER CHANGE =====
    -- Set by ChangeBillingTier when a downgrade is requested; the post-commit
    -- applier in api_billing/internal/handlers/jobs.go flips tier_id after the
    -- current invoice closes (status NOT IN ('draft','manual_review')) and
    -- clears these columns once cluster-access reconciliation succeeds.
    pending_tier_id UUID REFERENCES purser.billing_tiers(id),
    pending_effective_at TIMESTAMPTZ,
    pending_reason VARCHAR(50),

    -- ===== PAYMENT & BILLING =====
    payment_method VARCHAR(50),
    payment_reference VARCHAR(255),
	billing_name VARCHAR(255),            -- Customer legal/person name for invoices
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
    -- Mollie's authoritative next-payment date; the invoice job uses this as
    -- the period anchor so internal billing periods do not drift from Mollie.
    mollie_next_payment_date DATE,

    -- ===== X402 PROTOCOL =====
    x402_address_index INTEGER,
	x402_address_xpub TEXT,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),

	CONSTRAINT chk_billing_model CHECK (billing_model IN ('postpaid', 'prepaid')),
	CONSTRAINT chk_x402_address_derivation CHECK (
		(x402_address_index IS NULL) = (x402_address_xpub IS NULL)
	),
    UNIQUE(tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_pending_due
    ON purser.tenant_subscriptions(pending_effective_at)
    WHERE pending_tier_id IS NOT NULL;

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
    CONSTRAINT chk_subscription_pricing_meter CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$'),
    CONSTRAINT chk_subscription_pricing_model CHECK (model IS NULL OR model IN ('tiered_graduated', 'all_usage', 'dimensioned')),
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
    unit VARCHAR(32) NOT NULL DEFAULT '',
    dimensions JSONB NOT NULL DEFAULT '{}',
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
        'free_unmetered', 'self_hosted', 'included_subscription', 'beta_free'
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
-- A ledger row is sourced from a usage-priced invoice line, provider-keyed
-- storage usage, provider-keyed storage corrections, or a Stripe subscription
-- invoice for a monthly cluster. Monthly cluster revenue is collected entirely
-- on the Stripe side and never produces a row in purser.invoice_line_items.
--
-- Default status is 'held' so credits accumulate as a complete audit
-- trail without auto-promoting to payable. Operator vetting flips the
-- relevant rows to 'accruing' (or the writer can default to 'accruing'
-- when cluster_owners.status='approved' AND payout_eligible).
CREATE TABLE IF NOT EXISTS purser.operator_credit_ledger (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_type VARCHAR(32) NOT NULL DEFAULT 'invoice_line',
    invoice_line_item_id UUID REFERENCES purser.invoice_line_items(id) ON DELETE CASCADE,
    provider_usage_record_id UUID,
    usage_adjustment_id UUID,
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
        (source_type = 'invoice_line'            AND invoice_line_item_id IS NOT NULL) OR
        (source_type = 'provider_usage'          AND provider_usage_record_id IS NOT NULL) OR
        (source_type = 'usage_adjustment'        AND usage_adjustment_id IS NOT NULL) OR
        (source_type = 'stripe_subscription'     AND stripe_invoice_id    IS NOT NULL)
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
-- One accrual per provider-attributed storage usage slice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_provider_usage
    ON purser.operator_credit_ledger(provider_usage_record_id)
    WHERE entry_type = 'accrual' AND source_type = 'provider_usage';
-- One accrual per provider-attributed storage correction.
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_usage_adjustment
    ON purser.operator_credit_ledger(usage_adjustment_id)
    WHERE entry_type = 'accrual' AND source_type = 'usage_adjustment';
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
-- joins here). Marketplace listings, CreateClusterSubscription, and the
-- credit ledger use status='approved' AND payout_eligible to decide whether
-- operator revenue is payable or held.
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

-- Operator payout batches. Billing records accruals in operator_credit_ledger;
-- settlement tooling inserts payout batches and advances ledger rows through
-- eligible / paid_out status when funds are released.
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
    checkout_id VARCHAR(255),

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

-- Canonical usage records for billing calculations and usage APIs
CREATE TABLE IF NOT EXISTS purser.usage_records (
    -- ===== IDENTITY =====
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    
    -- ===== USAGE METRICS =====
    usage_type VARCHAR(64) NOT NULL,         -- canonical meter key or operational metric key
    unit VARCHAR(32) NOT NULL,
    dimensions JSONB NOT NULL DEFAULT '{}',
    dimension_key CHAR(64) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    report_id VARCHAR(64) NOT NULL,
    usage_value DECIMAL(20,6) NOT NULL DEFAULT 0,
    usage_details JSONB DEFAULT '{}',        -- Additional usage metadata
    -- value_kind labels the shape of usage_value. Rated billing rows are
    -- canonical 5-minute deltas; the writer must explicitly set 'delta'
    -- after passing the rated-meter validator. The fail-closed default
    -- catches any writer that bypasses validateUsageRecord — the row
    -- still lands (so debugging is possible) but billing excludes it
    -- because invoice aggregation requires value_kind = 'delta'.
    value_kind VARCHAR(20) NOT NULL DEFAULT 'ignored',
    
    -- ===== BILLING PERIOD =====
    period_start TIMESTAMP WITH TIME ZONE,   -- Granular start time
    period_end TIMESTAMP WITH TIME ZONE,     -- Granular end time
    granularity VARCHAR(20) NOT NULL DEFAULT 'minute_5',

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    CONSTRAINT usage_records_dimensioned_window_key UNIQUE
        (tenant_id, cluster_id, source_id, usage_type, dimension_key, period_start, period_end)
);

CREATE TABLE IF NOT EXISTS purser.provider_usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    usage_tenant_id UUID NOT NULL,
    work_cluster_id VARCHAR(100) NOT NULL,
    provider_tenant_id VARCHAR(100) NOT NULL DEFAULT '',
    provider_cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
    unit VARCHAR(32) NOT NULL,
    usage_value DECIMAL(20,6) NOT NULL,
    dimensions JSONB NOT NULL DEFAULT '{}',
    dimension_key CHAR(64) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    report_id VARCHAR(64) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    granularity VARCHAR(20) NOT NULL DEFAULT 'minute_5',
    value_kind VARCHAR(20) NOT NULL DEFAULT 'delta',
    source VARCHAR(64) NOT NULL DEFAULT 'kafka',
    usage_details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (
        usage_tenant_id, work_cluster_id,
        provider_tenant_id, provider_cluster_id,
        source_id, usage_type, dimension_key,
        period_start, period_end
    )
);

CREATE TABLE IF NOT EXISTS purser.usage_reports (
    report_id VARCHAR(64) PRIMARY KEY,
    report_kind VARCHAR(20) NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    source_region VARCHAR(64) NOT NULL DEFAULT '',
    sequence BIGINT NOT NULL,
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    complete BOOLEAN NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	CONSTRAINT chk_usage_reports_kind CHECK (report_kind IN ('finalized', 'reservation', 'window_complete')),
    UNIQUE (source_id, tenant_id, cluster_id, report_kind, sequence)
);

CREATE TABLE IF NOT EXISTS purser.usage_report_quarantine (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id VARCHAR(64),
    source_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_id UUID,
    rejected_reason VARCHAR(100) NOT NULL,
    source_topic TEXT NOT NULL DEFAULT '',
    source_partition INTEGER,
    source_offset BIGINT,
    raw_payload JSONB NOT NULL DEFAULT '{}',
    rejected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.usage_reservations (
    tenant_id UUID NOT NULL,
    source_id VARCHAR(128) NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    sequence BIGINT NOT NULL,
    report_id VARCHAR(64) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    meters JSONB NOT NULL,
    reserved_amount_micro BIGINT NOT NULL DEFAULT 0,
    currency CHAR(3) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, source_id, cluster_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_reservations_tenant_currency_recent
    ON purser.usage_reservations (tenant_id, currency, updated_at DESC)
    INCLUDE (reserved_amount_micro);

-- Marginal prepaid settlements against a billing-period cumulative rating.
-- The report key makes retries idempotent; amount_micro preserves sub-cent
-- precision and may be negative when an explicit correction creates credit.
CREATE TABLE IF NOT EXISTS purser.prepaid_usage_settlements (
    report_id VARCHAR(64) PRIMARY KEY,
    tenant_id UUID NOT NULL,
    billing_period_start TIMESTAMPTZ NOT NULL,
    billing_period_end TIMESTAMPTZ NOT NULL,
    amount_micro BIGINT NOT NULL,
    cumulative_amount_micro BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_prepaid_usage_settlements_tenant_period
    ON purser.prepaid_usage_settlements (tenant_id, billing_period_start, billing_period_end);

CREATE TABLE IF NOT EXISTS purser.metering_sources (
    source_id VARCHAR(128) PRIMARY KEY,
    region VARCHAR(64) NOT NULL DEFAULT '',
    active_from TIMESTAMPTZ NOT NULL,
    active_until TIMESTAMPTZ,
    required BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS purser.metering_windows (
    source_id VARCHAR(128) NOT NULL REFERENCES purser.metering_sources(source_id),
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    complete BOOLEAN NOT NULL,
    report_count BIGINT NOT NULL DEFAULT 0,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, period_start, period_end)
);

CREATE TABLE IF NOT EXISTS purser.metering_anomalies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id VARCHAR(128) NOT NULL,
    tenant_id UUID,
    cluster_id VARCHAR(100),
    anomaly_type VARCHAR(64) NOT NULL,
    source_event_id VARCHAR(255) NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'open',
    resolution_reason TEXT,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_id, anomaly_type, source_event_id),
    CONSTRAINT chk_metering_anomalies_status CHECK (status IN ('open', 'resolved', 'ignored'))
);

CREATE TABLE IF NOT EXISTS purser.usage_adjustments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
    unit VARCHAR(32) NOT NULL DEFAULT 'unit',
    dimensions JSONB NOT NULL DEFAULT '{}',
    dimension_key CHAR(64) NOT NULL DEFAULT repeat('0', 64),
    delta_value DECIMAL(20,6) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    value_kind VARCHAR(20) NOT NULL DEFAULT 'correction_delta',
    status VARCHAR(20) NOT NULL DEFAULT 'applied',
    source_system VARCHAR(64) NOT NULL,
    source_id VARCHAR(255) NOT NULL,
    reason VARCHAR(255),
    details JSONB NOT NULL DEFAULT '{}',
    applied_invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE SET NULL,
    balance_transaction_id UUID REFERENCES purser.balance_transactions(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_system, source_id),
    CONSTRAINT chk_usage_adjustments_status CHECK (status IN ('applied', 'ignored', 'pending')),
    CONSTRAINT chk_usage_adjustments_value_kind CHECK (value_kind = 'correction_delta')
);

-- ============================================================================
-- USAGE TRACKING INDEXES
-- ============================================================================

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_lookup ON purser.usage_records(tenant_id, cluster_id, usage_type);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_created_at ON purser.usage_records(created_at);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_period ON purser.usage_records(tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_granularity_period ON purser.usage_records(tenant_id, granularity, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_purser_usage_records_allowance
    ON purser.usage_records(tenant_id, usage_type, value_kind, granularity, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_provider_usage_provider_period
    ON purser.provider_usage_records(provider_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_provider_usage_tenant_period
    ON purser.provider_usage_records(usage_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_provider_usage_provider_cluster
    ON purser.provider_usage_records(provider_cluster_id, usage_type, period_start);
CREATE INDEX IF NOT EXISTS idx_usage_reports_source_period
    ON purser.usage_reports(source_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_usage_report_quarantine_source
    ON purser.usage_report_quarantine(source_id, rejected_at DESC);
CREATE INDEX IF NOT EXISTS idx_metering_anomalies_open
    ON purser.metering_anomalies(source_id, tenant_id, created_at)
    WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_usage_adjustments_invoice_lookup
    ON purser.usage_adjustments(tenant_id, period_start, period_end, status);
CREATE INDEX IF NOT EXISTS idx_usage_adjustments_allowance
    ON purser.usage_adjustments(tenant_id, usage_type, value_kind, status, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_usage_adjustments_source
    ON purser.usage_adjustments(source_system, source_id);
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
    WHERE status = 'pending' AND tx_id IS NULL;

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
    -- Format: per-meter object with model and unit_price required, plus
    -- optional included_quantity and config. Meter names
    -- are canonical usage_type keys, not a fixed enum:
    --   {
    --     "delivered_minutes":        {"unit_price": "0.0005", "model": "tiered_graduated", "included_quantity": "0"},
    --     "storage_gb_seconds_cold":  {"unit_price": "0.030", "model": "all_usage"},
    --     "ai_transcription_minutes": {"unit_price": "0.02", "model": "all_usage"}
    --   }
    -- Validated at write time by Purser's shared pricing validator.
    metered_rates JSONB DEFAULT '{}',

    -- ===== VISIBILITY & ACCESS RULES =====
    -- Minimum tier level required to see/subscribe to this cluster
    -- 0=no subscription required, 1=free, 2=supporter, 3=developer, 4=production, 5=enterprise
    required_tier_level INT DEFAULT 0,
    -- is_platform_official moved to Quartermaster (infrastructure_clusters.is_platform_official)
    allow_free_tier BOOLEAN DEFAULT FALSE,       -- If platform_official, allow free tier access

    -- ===== DEFAULT QUOTAS (billing metadata only) =====
    -- Format: {"retention_days": 7}
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
    dimensions JSONB NOT NULL DEFAULT '{}',
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE SET NULL,
    -- Nullable only for v0.2 outbox rows carried across the v0.3 upgrade;
    -- every v0.3 writer supplies the permanent invoice line identity.
    invoice_line_item_id UUID REFERENCES purser.invoice_line_items(id) ON DELETE CASCADE,
    sent_at TIMESTAMPTZ,
    attempt_count INT NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_stripe_meter_events_outbox_invoice_line
    ON purser.stripe_meter_events_outbox(invoice_line_item_id, stripe_meter_event_name);
CREATE INDEX IF NOT EXISTS idx_stripe_meter_events_outbox_pending
    ON purser.stripe_meter_events_outbox(created_at)
    WHERE sent_at IS NULL;

-- Customer invoice notifications are part of invoice finalization, not an
-- ephemeral post-commit side effect. The outbox row is created in the same
-- transaction as the permanent invoice and line-item snapshot. Purser workers
-- lease and retry delivery; drafts and manual-review holds never enter it.
CREATE TABLE IF NOT EXISTS purser.invoice_email_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    recipient TEXT NOT NULL,
    notification_type VARCHAR(32) NOT NULL DEFAULT 'invoice_created',
    reminder_stage INT NOT NULL DEFAULT 0,
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_invoice_email_outbox_notification
    ON purser.invoice_email_outbox(invoice_id, notification_type, reminder_stage);
CREATE INDEX IF NOT EXISTS idx_invoice_email_outbox_pending
    ON purser.invoice_email_outbox(next_attempt_at, created_at)
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
    ON purser.tenant_subscriptions(x402_address_xpub, x402_address_index)
    WHERE x402_address_index IS NOT NULL;

-- Nonce tracking to prevent replay attacks
-- Each EIP-3009 authorization has a unique nonce that can only be used once
CREATE TABLE IF NOT EXISTS purser.x402_nonces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Network + payer address + nonce = unique authorization
    network VARCHAR(20) NOT NULL,                 -- base, base-sepolia
    payer_address VARCHAR(42) NOT NULL,           -- 0x-prefixed address that signed
    nonce VARCHAR(78) NOT NULL,                   -- uint256 as hex string

    -- Embedded settlement hashes are computed and stored before broadcast;
    -- status advances to pending only after eth_sendRawTransaction acknowledges.
    tx_hash VARCHAR(66),                          -- Transaction hash (0x + 64 hex)
    tenant_id UUID NOT NULL,                      -- Tenant that received credit
    amount_cents BIGINT NOT NULL,                 -- Amount credited in cents
    settled_at TIMESTAMPTZ DEFAULT NOW(),         -- Row created at; pre-dates broadcast for 'submitting'
    submitted_at TIMESTAMPTZ,                     -- Set when eth_sendRawTransaction returns ok
    last_submit_attempt_at TIMESTAMPTZ,           -- Lease/timestamp for submit or resubmit attempts

    -- Durable signed payload (X402PaymentPayload as JSON). Used to verify
    -- idempotent retries and to resubmit a still-unused authorization if an
    -- earlier broadcast attempt failed before tx_hash was recorded.
    auth_payload JSONB,

    -- Reconciliation: track on-chain confirmation
    -- submitting: durable intent written; chain broadcast not yet acknowledged
    -- pending: tx submitted; no balance credit exists yet
    -- confirmed: receipt succeeded at required depth and balance credit committed
    -- failed: tx reverted or timed out without creating spendable credit
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    confirmed_at TIMESTAMPTZ,
    block_number BIGINT,
    gas_used BIGINT,
    failure_reason TEXT,
    client_ip INET,
    rollup_applied_at TIMESTAMPTZ,
    rollup_reversed_at TIMESTAMPTZ,

    UNIQUE(network, payer_address, nonce)
);

CREATE INDEX IF NOT EXISTS idx_x402_nonces_tenant ON purser.x402_nonces(tenant_id);
CREATE INDEX IF NOT EXISTS idx_x402_nonces_payer ON purser.x402_nonces(payer_address, network);
CREATE INDEX IF NOT EXISTS idx_x402_nonces_pending ON purser.x402_nonces(status, settled_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_x402_nonces_submitting ON purser.x402_nonces(settled_at) WHERE status = 'submitting';

-- Immutable x402 v2 offers. A signed EIP-3009 authorization does not carry a
-- FrameWorks tenant ID, so the quote binds the accepted requirements to the
-- tenant, resource, receiving address, amount, asset, chain, FX rate and
-- expiry before a facilitator is allowed to submit it.
CREATE TABLE IF NOT EXISTS purser.x402_payment_quotes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    resource TEXT NOT NULL,
    resource_class VARCHAR(32) NOT NULL,
    network VARCHAR(64) NOT NULL,
    asset VARCHAR(42) NOT NULL,
    pay_to VARCHAR(42) NOT NULL,
    amount_atomic NUMERIC(78, 0) NOT NULL CHECK (amount_atomic > 0),
    credit_amount_cents BIGINT NOT NULL CHECK (credit_amount_cents > 0),
    credit_currency CHAR(3) NOT NULL DEFAULT 'EUR',
    eur_per_usd_rate NUMERIC(20, 10) NOT NULL CHECK (eur_per_usd_rate > 0),
    requirements_json JSONB NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'offered' CHECK (
        status IN ('offered', 'claiming', 'settling', 'unknown', 'confirmed', 'expired', 'failed')
    ),
    claim_token UUID,
    claim_expires_at TIMESTAMPTZ,
    payment_fingerprint VARCHAR(64),
    provider VARCHAR(32),
    provider_transaction_id VARCHAR(255),
    failure_reason TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
	tax_document_kind VARCHAR(16) NOT NULL DEFAULT 'simplified' CHECK (tax_document_kind IN ('simplified', 'full')),
	tax_profile_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    confirmed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_x402_quotes_tenant_created
    ON purser.x402_payment_quotes(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_x402_quotes_reconcile
    ON purser.x402_payment_quotes(status, updated_at)
    WHERE status IN ('claiming', 'settling', 'unknown');
CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_quotes_payment_fingerprint
    ON purser.x402_payment_quotes(payment_fingerprint)
    WHERE payment_fingerprint IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_quotes_provider_transaction
    ON purser.x402_payment_quotes(provider, provider_transaction_id)
    WHERE provider IS NOT NULL AND provider_transaction_id IS NOT NULL;

ALTER TABLE purser.x402_nonces
    ADD COLUMN IF NOT EXISTS quote_id UUID REFERENCES purser.x402_payment_quotes(id),
    ADD COLUMN IF NOT EXISTS settlement_provider VARCHAR(32),
    ADD COLUMN IF NOT EXISTS provider_transaction_id VARCHAR(255);

CREATE UNIQUE INDEX IF NOT EXISTS idx_x402_nonces_quote
    ON purser.x402_nonces(quote_id)
    WHERE quote_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.x402_settlement_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    settlement_id UUID NOT NULL REFERENCES purser.x402_nonces(id),
    attempt_number INTEGER NOT NULL DEFAULT 1 CHECK (attempt_number > 0),
    network VARCHAR(20) NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    relayer_address VARCHAR(42) NOT NULL,
    relayer_nonce BIGINT NOT NULL CHECK (relayer_nonce >= 0),
    signed_raw_transaction BYTEA NOT NULL,
    transaction_hash VARCHAR(66) NOT NULL,
    gas_limit BIGINT NOT NULL CHECK (gas_limit > 0),
    max_fee_per_gas NUMERIC(78,0) NOT NULL CHECK (max_fee_per_gas > 0),
    max_priority_fee_per_gas NUMERIC(78,0) NOT NULL CHECK (max_priority_fee_per_gas >= 0),
    state VARCHAR(24) NOT NULL DEFAULT 'prepared' CHECK (
        state IN ('prepared', 'broadcast_unknown', 'broadcast', 'confirmed', 'reverted', 'replaced')
    ),
    broadcast_attempts INTEGER NOT NULL DEFAULT 0 CHECK (broadcast_attempts >= 0),
    last_error TEXT,
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_broadcast_at TIMESTAMPTZ,
    last_broadcast_at TIMESTAMPTZ,
    confirmed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (settlement_id, attempt_number),
    UNIQUE (network, relayer_address, relayer_nonce),
    UNIQUE (transaction_hash)
);

CREATE INDEX IF NOT EXISTS idx_x402_settlement_attempts_reconcile
    ON purser.x402_settlement_attempts(state, updated_at)
    WHERE state IN ('prepared', 'broadcast_unknown', 'broadcast');

-- Conditions such as a consumed authorization without a recoverable
-- transaction hash require durable operator action rather than log scraping.
CREATE TABLE IF NOT EXISTS purser.crypto_accounting_anomalies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kind VARCHAR(64) NOT NULL,
    network VARCHAR(64) NOT NULL,
    reference_type VARCHAR(64) NOT NULL,
    reference_id VARCHAR(255) NOT NULL,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    currency CHAR(3) NOT NULL DEFAULT 'EUR',
    detail TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    occurrences INTEGER NOT NULL DEFAULT 1 CHECK (occurrences > 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    resolved_by UUID,
    resolution_note TEXT,
    UNIQUE(kind, reference_type, reference_id)
);

CREATE INDEX IF NOT EXISTS idx_crypto_accounting_anomalies_open
    ON purser.crypto_accounting_anomalies(last_seen_at, tenant_id)
    WHERE status = 'open';

CREATE TABLE IF NOT EXISTS purser.x402_rate_limit_windows (
    scope VARCHAR(32) NOT NULL,
    identity_hash BYTEA NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    request_count INTEGER NOT NULL CHECK (request_count > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(scope, identity_hash, window_started_at)
);

CREATE INDEX IF NOT EXISTS idx_x402_rate_limit_windows_expiry
    ON purser.x402_rate_limit_windows(window_started_at);

CREATE TABLE IF NOT EXISTS purser.x402_mutation_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    quote_id UUID NOT NULL REFERENCES purser.x402_payment_quotes(id),
    idempotency_key VARCHAR(255) NOT NULL,
    request_fingerprint VARCHAR(64) NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    protocol VARCHAR(12) NOT NULL CHECK (protocol IN ('http', 'mcp')),
    operation VARCHAR(255) NOT NULL,
	status VARCHAR(24) NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed', 'completion_pending', 'completed', 'operator_review')),
	result BYTEA,
	content_type VARCHAR(255),
	status_code INTEGER,
	attempt_count INTEGER NOT NULL DEFAULT 1 CHECK (attempt_count > 0),
	last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	review_reason TEXT,
	resolved_by UUID,
	resolved_at TIMESTAMPTZ,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, idempotency_key),
    UNIQUE(quote_id)
);

CREATE INDEX IF NOT EXISTS idx_x402_mutation_results_unknown
    ON purser.x402_mutation_results(claimed_at)
	WHERE status IN ('claimed', 'completion_pending', 'operator_review');

-- ============================================================================
-- SIMPLIFIED INVOICES (EU VAT COMPLIANCE)
-- ============================================================================
-- For payments under €100, EU law allows simplified invoices without
-- customer details. We store these for 10-year retention requirement.
-- ============================================================================

CREATE SEQUENCE IF NOT EXISTS purser.simplified_invoice_number_seq;
CREATE SEQUENCE IF NOT EXISTS purser.credit_note_number_seq;

CREATE TABLE IF NOT EXISTS purser.vat_rate_periods (
    country_code CHAR(2) NOT NULL,
    rate_bps INTEGER NOT NULL CHECK (rate_bps >= 0 AND rate_bps <= 10000),
    effective_from DATE NOT NULL,
    effective_until DATE,
    source VARCHAR(255) NOT NULL,
    source_reference TEXT NOT NULL,
    source_checked_on DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(country_code, effective_from),
    CHECK (effective_until IS NULL OR effective_until > effective_from)
);

INSERT INTO purser.vat_rate_periods
    (country_code, rate_bps, effective_from, source, source_reference, source_checked_on)
SELECT country_code, rate_bps, DATE '2026-01-01',
       'EU Your Europe standard VAT rates',
       'https://europa.eu/youreurope/business/taxation/vat/vat-rules-rates/index_en.htm',
       DATE '2026-07-13'
FROM (VALUES
    ('AT',2000),('BE',2100),('BG',2000),('HR',2500),('CY',1900),
    ('CZ',2100),('DK',2500),('EE',2400),('FI',2550),('FR',2000),
    ('DE',1900),('GR',2400),('HU',2700),('IE',2300),('IT',2200),
    ('LV',2100),('LT',2100),('LU',1700),('MT',1800),('NL',2100),
    ('PL',2300),('PT',2300),('RO',2100),('SK',2300),('SI',2200),
    ('ES',2100),('SE',2500)
) AS rates(country_code, rate_bps)
ON CONFLICT (country_code, effective_from) DO NOTHING;

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
    vat_rate_source VARCHAR(255),
    vat_rate_table_checked_on DATE,
    vat_rate_effective_from DATE,
    tax_validation_status VARCHAR(32) NOT NULL DEFAULT 'not_validated',
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',

    -- EUR equivalent (for threshold tracking)
    amount_eur_cents BIGINT NOT NULL,             -- Converted at ECB rate
    ecb_rate DECIMAL(10,6),                       -- EUR/USD rate used
    fx_rate_source VARCHAR(255),
    fx_rate_observed_at TIMESTAMPTZ,

    -- Location evidence (2 pieces required for EU VAT)
    evidence_ip_country VARCHAR(2),               -- ISO country from IP
    evidence_wallet_network VARCHAR(20),          -- Blockchain network
    evidence_billing_country VARCHAR(2),
    evidence_status VARCHAR(24) NOT NULL DEFAULT 'missing' CONSTRAINT chk_simplified_invoice_evidence_status CHECK (
        evidence_status IN ('complete', 'single_source', 'conflict', 'missing')
    ),
    evidence_conflict BOOLEAN NOT NULL DEFAULT FALSE,
    tax_policy_ref VARCHAR(255),

    -- Supplier details (us - static but stored for audit)
    supplier_name VARCHAR(255) NOT NULL,
    supplier_address TEXT NOT NULL,
    supplier_vat_number VARCHAR(50) NOT NULL,
	supplier_registration_number VARCHAR(100),
	service_description TEXT,
	service_quantity INTEGER CONSTRAINT chk_simplified_invoice_service_quantity CHECK (service_quantity > 0),
	service_date DATE,

    -- Invoice date
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    created_at TIMESTAMPTZ DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years')
);

CREATE SEQUENCE IF NOT EXISTS purser.crypto_invoice_number_seq;

CREATE TABLE IF NOT EXISTS purser.crypto_invoices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_number VARCHAR(50) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    reference_type VARCHAR(20) NOT NULL,
    reference_id VARCHAR(255) NOT NULL,
    gross_amount_cents BIGINT NOT NULL,
    net_amount_cents BIGINT NOT NULL,
    vat_amount_cents BIGINT NOT NULL,
    vat_rate_bps INTEGER NOT NULL,
    vat_rate_source VARCHAR(255),
    vat_rate_table_checked_on DATE,
    vat_rate_effective_from DATE,
    tax_validation_status VARCHAR(32) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    amount_eur_cents BIGINT NOT NULL,
    ecb_rate DECIMAL(10,6),
    fx_rate_source VARCHAR(255),
    fx_rate_observed_at TIMESTAMPTZ,
    evidence_ip_country VARCHAR(2),
    evidence_wallet_network VARCHAR(20),
    evidence_billing_country VARCHAR(2),
    evidence_status VARCHAR(24) NOT NULL CONSTRAINT chk_crypto_invoice_evidence_status CHECK (
        evidence_status IN ('complete', 'single_source', 'conflict', 'missing')
    ),
    evidence_conflict BOOLEAN NOT NULL DEFAULT FALSE,
    tax_policy_ref VARCHAR(255),
    supplier_name VARCHAR(255) NOT NULL,
    supplier_address TEXT NOT NULL,
    supplier_vat_number VARCHAR(50) NOT NULL,
	supplier_registration_number VARCHAR(100) NOT NULL,
	service_description TEXT NOT NULL,
	service_quantity INTEGER NOT NULL CHECK (service_quantity > 0),
	service_date DATE NOT NULL,
    customer_email VARCHAR(255) NOT NULL,
	customer_name VARCHAR(255) NOT NULL,
    customer_company VARCHAR(255),
    customer_address JSONB NOT NULL,
    customer_vat_number VARCHAR(100),
    customer_vat_validated BOOLEAN NOT NULL DEFAULT FALSE,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years'),
    UNIQUE (tenant_id, reference_type, reference_id)
);

CREATE TABLE IF NOT EXISTS purser.credit_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    credit_note_number VARCHAR(50) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    source_document_type VARCHAR(32) NOT NULL CHECK (source_document_type IN ('invoice', 'simplified_invoice', 'crypto_invoice')),
    source_document_id UUID NOT NULL,
    reversal_reference_type VARCHAR(64) NOT NULL,
    reversal_reference_id VARCHAR(255) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    currency CHAR(3) NOT NULL,
    reason TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '10 years'),
    UNIQUE(source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
);

CREATE OR REPLACE FUNCTION purser.prevent_billing_document_early_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.retention_until > NOW() THEN
        RAISE EXCEPTION 'billing document % is retained until %', OLD.id, OLD.retention_until;
    END IF;
    RETURN OLD;
END;
$$;

CREATE TRIGGER billing_invoices_retention_guard
    BEFORE DELETE ON purser.billing_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
CREATE TRIGGER billing_payments_retention_guard
    BEFORE DELETE ON purser.billing_payments
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
CREATE TRIGGER simplified_invoices_retention_guard
    BEFORE DELETE ON purser.simplified_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

CREATE TRIGGER crypto_invoices_retention_guard
    BEFORE DELETE ON purser.crypto_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
CREATE TRIGGER credit_notes_retention_guard
    BEFORE DELETE ON purser.credit_notes
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

CREATE INDEX IF NOT EXISTS idx_credit_notes_tenant_issued
    ON purser.credit_notes(tenant_id, issued_at DESC);

CREATE INDEX IF NOT EXISTS idx_simplified_invoices_tenant ON purser.simplified_invoices(tenant_id);
CREATE INDEX IF NOT EXISTS idx_simplified_invoices_issued ON purser.simplified_invoices(issued_at);
CREATE INDEX IF NOT EXISTS idx_simplified_invoices_reference ON purser.simplified_invoices(reference_type, reference_id);
CREATE INDEX IF NOT EXISTS idx_crypto_invoices_tenant ON purser.crypto_invoices(tenant_id, issued_at DESC);
CREATE INDEX IF NOT EXISTS idx_crypto_invoices_reference ON purser.crypto_invoices(reference_type, reference_id);

CREATE TABLE IF NOT EXISTS purser.vat_validation_evidence (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    country_code VARCHAR(2) NOT NULL,
    vat_number_hash VARCHAR(64) NOT NULL,
    vat_number_masked VARCHAR(32) NOT NULL,
    provider VARCHAR(16) NOT NULL DEFAULT 'VIES',
    valid BOOLEAN NOT NULL,
    request_date DATE NOT NULL,
    trader_name TEXT,
    trader_address TEXT,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    raw_response JSONB NOT NULL,
    UNIQUE(tenant_id, vat_number_hash, request_date)
);

CREATE INDEX IF NOT EXISTS idx_vat_validation_evidence_current
    ON purser.vat_validation_evidence(tenant_id, expires_at DESC);

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
  ADD COLUMN IF NOT EXISTS processes_dvr JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_clip JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_dvr_finalize JSONB DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS processes_vod JSONB DEFAULT '[]';

-- ============================================================================
-- PAYMENT PROVIDER INTENTS & RECONCILIATION
-- ============================================================================
-- Durable pre-provider-call intent rows, attempt history, provider object
-- mapping, and reversal ledger that underpin Stripe/Mollie correctness.
-- Inserted before every external side effect so a crash never leaves an
-- orphan provider object.

CREATE TABLE IF NOT EXISTS purser.payment_provider_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    provider VARCHAR(20) NOT NULL,
    purpose VARCHAR(40) NOT NULL,
    local_reference_type VARCHAR(40),
    local_reference_id UUID,
    provider_customer_id VARCHAR(255),
    provider_session_id VARCHAR(255),
    provider_subscription_id VARCHAR(255),
    provider_payment_id VARCHAR(255),
    status VARCHAR(40) NOT NULL DEFAULT 'pending',
    currency CHAR(3) NOT NULL,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    idempotency_key VARCHAR(128) NOT NULL,
    last_error TEXT,
    attempt_count INT NOT NULL DEFAULT 0,
    succeeded_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_payment_intent_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_payment_intent_purpose CHECK (purpose IN (
        'tenant_subscription_checkout',
        'cluster_subscription_checkout',
        'mollie_first_payment',
        'mollie_subscription_create',
        'stripe_overage_charge',
        'mollie_overage_charge',
        'prepaid_topup'
    )),
    CONSTRAINT chk_payment_intent_status CHECK (status IN (
        'pending', 'provider_open', 'sca_required',
        'succeeded', 'expired', 'cancelled',
        'provider_call_failed', 'terminal_failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_idem
    ON purser.payment_provider_intents(provider, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_payment_provider_intents_tenant
    ON purser.payment_provider_intents(tenant_id, purpose, status);
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_session
    ON purser.payment_provider_intents(provider, provider_session_id)
    WHERE provider_session_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_payment
    ON purser.payment_provider_intents(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_provider_intents_subscription
    ON purser.payment_provider_intents(provider, provider_subscription_id)
    WHERE provider_subscription_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payment_provider_intents_local_ref
    ON purser.payment_provider_intents(local_reference_type, local_reference_id)
    WHERE local_reference_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.provider_payment_objects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider VARCHAR(20) NOT NULL,
    object_type VARCHAR(40) NOT NULL,
    provider_object_id VARCHAR(255) NOT NULL,
    tenant_id UUID,
    local_reference_type VARCHAR(40),
    local_reference_id UUID,
    intent_id UUID REFERENCES purser.payment_provider_intents(id),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_provider_payment_object_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_provider_payment_object_type CHECK (object_type IN (
        'customer', 'subscription', 'invoice',
        'payment_intent', 'charge', 'refund', 'dispute',
        'payment', 'mandate', 'chargeback'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_payment_objects
    ON purser.provider_payment_objects(provider, object_type, provider_object_id);
CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_tenant
    ON purser.provider_payment_objects(tenant_id, object_type)
    WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_local_ref
    ON purser.provider_payment_objects(local_reference_type, local_reference_id)
    WHERE local_reference_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_payment_objects_intent
    ON purser.provider_payment_objects(intent_id)
    WHERE intent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.billing_payment_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id UUID NOT NULL REFERENCES purser.billing_payments(id) ON DELETE CASCADE,
    intent_id UUID REFERENCES purser.payment_provider_intents(id),
    attempt_number INT NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    provider VARCHAR(20) NOT NULL,
    provider_payment_id VARCHAR(255),
    status VARCHAR(40) NOT NULL DEFAULT 'pending',
    failure_code VARCHAR(100),
    failure_message TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_billing_payment_attempt_provider CHECK (provider IN ('stripe', 'mollie')),
    CONSTRAINT chk_billing_payment_attempt_status CHECK (status IN (
        'pending', 'provider_open', 'sca_required',
        'succeeded', 'failed', 'expired', 'cancelled',
        'provider_call_failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_payment_attempts_seq
    ON purser.billing_payment_attempts(payment_id, attempt_number);
CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_payment_attempts_idem
    ON purser.billing_payment_attempts(provider, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_billing_payment_attempts_next_retry
    ON purser.billing_payment_attempts(next_retry_at, status)
    WHERE next_retry_at IS NOT NULL AND status = 'provider_call_failed';
CREATE INDEX IF NOT EXISTS idx_billing_payment_attempts_provider_id
    ON purser.billing_payment_attempts(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purser.payment_reversals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    payment_id UUID REFERENCES purser.billing_payments(id) ON DELETE SET NULL,
    pending_topup_id UUID REFERENCES purser.pending_topups(id) ON DELETE SET NULL,
    invoice_id UUID REFERENCES purser.billing_invoices(id) ON DELETE SET NULL,
    balance_transaction_id UUID REFERENCES purser.balance_transactions(id) ON DELETE SET NULL,
    operator_credit_ledger_id UUID REFERENCES purser.operator_credit_ledger(id) ON DELETE SET NULL,
    provider VARCHAR(20) NOT NULL,
    reversal_type VARCHAR(20) NOT NULL,
    provider_reversal_id VARCHAR(255) NOT NULL,
    provider_charge_id VARCHAR(255),
    amount_cents BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    reason TEXT,
    operator_review_required BOOLEAN NOT NULL DEFAULT FALSE,
    actor_id UUID,
    actor_kind VARCHAR(20),
    evidence_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_payment_reversal_provider CHECK (provider IN ('stripe', 'mollie', 'manual')),
    CONSTRAINT chk_payment_reversal_type CHECK (reversal_type IN ('refund', 'dispute', 'chargeback', 'manual')),
    CONSTRAINT chk_payment_reversal_status CHECK (status IN ('pending', 'succeeded', 'failed', 'needs_review'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_reversals_provider_id
    ON purser.payment_reversals(provider, provider_reversal_id);
CREATE INDEX IF NOT EXISTS idx_payment_reversals_tenant
    ON purser.payment_reversals(tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_reversals_invoice
    ON purser.payment_reversals(invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payment_reversals_review
    ON purser.payment_reversals(tenant_id) WHERE operator_review_required;

CREATE TABLE IF NOT EXISTS purser.operator_credit_clawback_reversals (
    payment_reversal_id UUID NOT NULL REFERENCES purser.payment_reversals(id) ON DELETE CASCADE,
    operator_credit_ledger_id UUID NOT NULL REFERENCES purser.operator_credit_ledger(id) ON DELETE CASCADE,
    accrual_ledger_id UUID NOT NULL REFERENCES purser.operator_credit_ledger(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (payment_reversal_id, accrual_ledger_id),
    UNIQUE (operator_credit_ledger_id)
);

CREATE TABLE IF NOT EXISTS purser.mollie_payment_observations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    mollie_payment_id VARCHAR(50) NOT NULL,
    mollie_subscription_id VARCHAR(50),
    mollie_mandate_id VARCHAR(50),
    sequence_type VARCHAR(20),
    status VARCHAR(20) NOT NULL,
    amount_cents BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    paid_at TIMESTAMPTZ,
    invoice_id UUID REFERENCES purser.billing_invoices(id),
    payment_id UUID REFERENCES purser.billing_payments(id),
    resolved_at TIMESTAMPTZ,
    resolution VARCHAR(40),
    attempt_count INT NOT NULL DEFAULT 0,
    last_error TEXT,
    raw_payload BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_mollie_obs_status CHECK (status IN (
        'open', 'pending', 'paid', 'failed', 'expired', 'cancelled', 'authorized'
    )),
    CONSTRAINT chk_mollie_obs_resolution CHECK (resolution IS NULL OR resolution IN (
        'attached', 'no_local_invoice', 'mandate_revoked', 'ignored', 'failed'
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_mollie_observations_payment
    ON purser.mollie_payment_observations(mollie_payment_id);
CREATE INDEX IF NOT EXISTS idx_mollie_observations_unresolved
    ON purser.mollie_payment_observations(tenant_id, mollie_subscription_id)
    WHERE resolved_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_mollie_observations_invoice
    ON purser.mollie_payment_observations(invoice_id)
    WHERE invoice_id IS NOT NULL;

-- ============================================================================
-- IDEMPOTENT EXTENSIONS for existing payment tables
-- ============================================================================

ALTER TABLE purser.webhook_events
    ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'processed',
    ADD COLUMN IF NOT EXISTS raw_payload BYTEA,
    ADD COLUMN IF NOT EXISTS signature_header TEXT,
    ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS provider_object_id VARCHAR(255);

ALTER TABLE purser.webhook_events
    ADD CONSTRAINT chk_webhook_events_status CHECK (status IN (
        'claimed', 'processed', 'failed_retryable', 'failed_terminal', 'blocked'
    )) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_webhook_events_status_received
    ON purser.webhook_events(status, received_at)
    WHERE status IN ('claimed', 'failed_retryable', 'blocked');
CREATE INDEX IF NOT EXISTS idx_webhook_events_provider_object
    ON purser.webhook_events(provider, provider_object_id)
    WHERE provider_object_id IS NOT NULL;

-- Provider ingress is acknowledged after validation and this durable insert.
-- Reconciliation workers lease rows independently of the HTTP/gRPC request.
CREATE TABLE IF NOT EXISTS purser.provider_webhook_inbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider VARCHAR(50) NOT NULL,
    event_key VARCHAR(320) NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}',
    raw_payload BYTEA NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    last_error TEXT,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(provider, event_key),
    CONSTRAINT chk_provider_webhook_inbox_status
        CHECK (status IN ('pending', 'processing', 'failed', 'processed'))
);

CREATE INDEX IF NOT EXISTS idx_provider_webhook_inbox_pending
    ON purser.provider_webhook_inbox(next_attempt_at, created_at)
    WHERE status IN ('pending', 'failed', 'processing');

ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS reopened_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS confirmed_paid_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reversed_paid_cents BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_billing_invoices_reopened
    ON purser.billing_invoices(tenant_id, reopened_at)
    WHERE reopened_at IS NOT NULL;

ALTER TABLE purser.billing_payments
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id),
    ADD COLUMN IF NOT EXISTS reversed_amount_cents BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_billing_payments_intent
    ON purser.billing_payments(intent_id)
    WHERE intent_id IS NOT NULL;

ALTER TABLE purser.balance_transactions
    ADD COLUMN IF NOT EXISTS actor_id UUID,
    ADD COLUMN IF NOT EXISTS actor_kind VARCHAR(20),
    ADD COLUMN IF NOT EXISTS reason TEXT,
    ADD COLUMN IF NOT EXISTS evidence_ref TEXT,
    ADD COLUMN IF NOT EXISTS reverses_transaction_id UUID REFERENCES purser.balance_transactions(id);

ALTER TABLE purser.balance_transactions
    ADD CONSTRAINT chk_balance_transactions_actor_kind CHECK (
        actor_kind IS NULL OR actor_kind IN ('user', 'system', 'webhook', 'job')
    ) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_balance_transactions_reverses
    ON purser.balance_transactions(reverses_transaction_id)
    WHERE reverses_transaction_id IS NOT NULL;

ALTER TABLE purser.pending_topups
    ADD COLUMN IF NOT EXISTS provider_payment_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS provider_charge_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS refunded_amount_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id);

ALTER TABLE purser.pending_topups
    ALTER COLUMN checkout_id DROP NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_pending_topups_provider_payment
    ON purser.pending_topups(provider, provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pending_topups_intent
    ON purser.pending_topups(intent_id)
    WHERE intent_id IS NOT NULL;

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS pending_intent_id UUID REFERENCES purser.payment_provider_intents(id);

CREATE INDEX IF NOT EXISTS idx_tenant_subscriptions_pending_intent
    ON purser.tenant_subscriptions(pending_intent_id)
    WHERE pending_intent_id IS NOT NULL;

ALTER TABLE purser.cluster_subscriptions
    ADD COLUMN IF NOT EXISTS intent_id UUID REFERENCES purser.payment_provider_intents(id);

CREATE INDEX IF NOT EXISTS idx_cluster_subscriptions_intent
    ON purser.cluster_subscriptions(intent_id)
    WHERE intent_id IS NOT NULL;

-- Read-only reconciliation views for payment-state drift checks.
CREATE OR REPLACE VIEW purser.payment_report_provider_objects_without_local_rows AS
SELECT ppo.id,
       ppo.provider,
       ppo.object_type,
       ppo.provider_object_id,
       ppo.tenant_id,
       ppo.local_reference_type,
       ppo.local_reference_id,
       ppo.intent_id,
       ppo.metadata,
       ppo.created_at,
       ppo.updated_at
FROM purser.provider_payment_objects ppo
LEFT JOIN purser.billing_payments bp
    ON ppo.local_reference_type = 'payment'
   AND ppo.local_reference_id = bp.id
LEFT JOIN purser.pending_topups pt
    ON ppo.local_reference_type = 'topup'
   AND ppo.local_reference_id = pt.id
LEFT JOIN purser.payment_provider_intents ppi
    ON ppo.local_reference_type = 'intent'
   AND ppo.local_reference_id = ppi.id
WHERE ppo.local_reference_id IS NOT NULL
  AND (
      (ppo.local_reference_type = 'payment' AND bp.id IS NULL)
      OR (ppo.local_reference_type = 'topup' AND pt.id IS NULL)
      OR (ppo.local_reference_type = 'intent' AND ppi.id IS NULL)
      OR (ppo.local_reference_type NOT IN ('payment', 'topup', 'intent'))
  );

CREATE OR REPLACE VIEW purser.payment_report_pending_failed_intents AS
SELECT id,
       tenant_id,
       provider,
       purpose,
       local_reference_type,
       local_reference_id,
       provider_customer_id,
       provider_session_id,
       provider_subscription_id,
       provider_payment_id,
       status,
       currency,
       amount_cents,
       idempotency_key,
       last_error,
       attempt_count,
       succeeded_at,
       expires_at,
       created_at,
       updated_at
FROM purser.payment_provider_intents
WHERE status IN ('pending', 'provider_open', 'sca_required', 'provider_call_failed', 'terminal_failed')
  AND updated_at < NOW() - INTERVAL '15 minutes';

CREATE OR REPLACE VIEW purser.payment_report_paid_invoice_amount_mismatch AS
WITH confirmed AS (
    SELECT invoice_id,
           currency,
           SUM((amount * 100)::bigint) AS confirmed_payment_cents
    FROM purser.billing_payments
    WHERE status = 'confirmed'
    GROUP BY invoice_id, currency
),
reversed AS (
    SELECT invoice_id,
           currency,
           SUM(amount_cents) AS reversed_payment_cents
    FROM purser.payment_reversals
    WHERE status = 'succeeded'
    GROUP BY invoice_id, currency
)
SELECT bi.id AS invoice_id,
       bi.tenant_id,
       bi.currency,
       (bi.amount * 100)::bigint AS invoice_amount_cents,
       COALESCE(c.confirmed_payment_cents, 0) AS confirmed_payment_cents,
       COALESCE(r.reversed_payment_cents, 0) AS reversed_payment_cents
FROM purser.billing_invoices bi
LEFT JOIN confirmed c ON c.invoice_id = bi.id AND c.currency = bi.currency
LEFT JOIN reversed r ON r.invoice_id = bi.id AND r.currency = bi.currency
WHERE bi.status = 'paid'
  AND COALESCE(c.confirmed_payment_cents, 0) - COALESCE(r.reversed_payment_cents, 0) <> (bi.amount * 100)::bigint;

CREATE OR REPLACE VIEW purser.payment_report_reversals_without_payment_rows AS
SELECT pr.id,
       pr.tenant_id,
       pr.payment_id,
       pr.pending_topup_id,
       pr.invoice_id,
       pr.balance_transaction_id,
       pr.operator_credit_ledger_id,
       pr.provider,
       pr.reversal_type,
       pr.provider_reversal_id,
       pr.provider_charge_id,
       pr.amount_cents,
       pr.currency,
       pr.status,
       pr.reason,
       pr.operator_review_required,
       pr.actor_id,
       pr.actor_kind,
       pr.evidence_ref,
       pr.created_at,
       pr.updated_at
FROM purser.payment_reversals pr
LEFT JOIN purser.billing_payments bp ON pr.payment_id = bp.id
LEFT JOIN purser.pending_topups pt ON pr.pending_topup_id = pt.id
WHERE (pr.payment_id IS NOT NULL AND bp.id IS NULL)
   OR (pr.pending_topup_id IS NOT NULL AND pt.id IS NULL)
   OR (pr.payment_id IS NULL AND pr.pending_topup_id IS NULL);

CREATE OR REPLACE VIEW purser.payment_report_prepaid_negative_balances AS
SELECT id,
       tenant_id,
       balance_cents,
       balance_remainder_micro,
       currency,
       low_balance_threshold_cents,
       created_at,
       updated_at
FROM purser.prepaid_balances
WHERE balance_cents < 0;

CREATE OR REPLACE VIEW purser.payment_report_operator_credits_without_clawback AS
SELECT
    accrual.id,
    accrual.source_type,
    accrual.invoice_line_item_id,
    accrual.provider_usage_record_id,
    accrual.usage_adjustment_id,
    accrual.stripe_invoice_id,
    accrual.entry_type,
    accrual.reverses_ledger_id,
    accrual.cluster_owner_tenant_id,
    accrual.cluster_id,
    accrual.invoice_id,
    accrual.period_start,
    accrual.period_end,
    accrual.currency,
    accrual.gross_cents,
    accrual.platform_fee_cents,
    accrual.payable_cents,
    accrual.status,
    accrual.payout_batch_id,
    accrual.notes,
    accrual.created_at,
    accrual.updated_at
FROM purser.operator_credit_ledger accrual
JOIN purser.payment_reversals pr
    ON pr.invoice_id = accrual.invoice_id
   AND pr.status = 'succeeded'
LEFT JOIN purser.operator_credit_ledger clawback
    ON clawback.reverses_ledger_id = accrual.id
   AND clawback.entry_type = 'clawback'
WHERE accrual.entry_type = 'accrual'
  AND clawback.id IS NULL;

CREATE OR REPLACE VIEW purser.payment_report_stripe_meter_outbox_stuck AS
SELECT
    id,
    tenant_id,
    cluster_id,
    meter,
    stripe_meter_event_name,
    quantity,
    dimensions,
    period_start,
    period_end,
    invoice_id,
    invoice_line_item_id,
    sent_at,
    attempt_count,
    last_error,
    created_at,
    updated_at
FROM purser.stripe_meter_events_outbox
WHERE sent_at IS NULL
  AND attempt_count >= 5;

CREATE OR REPLACE VIEW purser.payment_report_invoice_email_outbox_stuck AS
SELECT id, invoice_id, tenant_id, recipient, attempts, next_attempt_at,
       last_error, created_at, updated_at, notification_type, reminder_stage
FROM purser.invoice_email_outbox
WHERE sent_at IS NULL
  AND attempts >= 5;

CREATE OR REPLACE VIEW purser.payment_report_webhook_inbox_stuck AS
SELECT id, provider, event_key, status, attempts, next_attempt_at,
       last_error, created_at, updated_at
FROM purser.provider_webhook_inbox
WHERE status <> 'processed'
  AND attempts >= 5;

-- ============================================================================
-- BILLING EVENT OUTBOX
-- ============================================================================
-- Durable outbox for Purser-emitted service events to Decklog. Producers
-- write a row in the same DB transaction as the billing mutation; a drain
-- worker dispatches with exponential backoff.

CREATE TABLE IF NOT EXISTS purser.billing_event_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type    TEXT NOT NULL,
    tenant_id     UUID NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    billing_event JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_purser_billing_event_outbox_pending
    ON purser.billing_event_outbox(created_at)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_purser_billing_event_outbox_tenant
    ON purser.billing_event_outbox(tenant_id, created_at DESC);

-- ============================================================================
-- Meter validation surface
-- ----------------------------------------------------------------------------
-- The canonical billing path stamps every usage_records row with value_kind
-- and runs per-record validation; mismatched submissions land in
-- purser.usage_records_quarantine instead of usage_records.
-- See docs/architecture/meter-contracts.md.
-- ============================================================================

-- value_kind labels what shape of value a usage_records row carries. Fail
-- closed for fresh and upgraded schemas: producers must explicitly stamp
-- 'delta' before a rated row can enter invoice aggregation.
ALTER TABLE purser.usage_records
    ADD COLUMN IF NOT EXISTS value_kind VARCHAR(20) NOT NULL DEFAULT 'ignored';

-- Quarantine for rated submissions that fail the validator. Rows here
-- are not billed; operators inspect via /admin/billing/quarantine.
CREATE TABLE IF NOT EXISTS purser.usage_records_quarantine (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
    usage_value DECIMAL(20,6) NOT NULL DEFAULT 0,
    usage_details JSONB DEFAULT '{}',
    period_start TIMESTAMP WITH TIME ZONE,
    period_end TIMESTAMP WITH TIME ZONE,
    granularity VARCHAR(20) NOT NULL DEFAULT '',
    value_kind VARCHAR(20),
    rejected_reason VARCHAR(100) NOT NULL,     -- 'unknown_meter' | 'granularity_mismatch' | 'value_kind_mismatch' | 'period_misaligned'
    rejected_at TIMESTAMP NOT NULL DEFAULT NOW(),
    source TEXT NOT NULL DEFAULT '',           -- 'kafka' | 'http' | etc.
    raw_payload JSONB DEFAULT '{}'             -- original submission for forensic replay
);

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_quarantine_tenant
    ON purser.usage_records_quarantine(tenant_id, rejected_at DESC);

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_quarantine_reason
    ON purser.usage_records_quarantine(rejected_reason, rejected_at DESC);

-- ============================================================================
-- Canonical-meter constraints
-- ----------------------------------------------------------------------------
-- Runs idempotently every schema-init. Pricing rule meter names are data, not
-- enum values: marketplace operators and future advanced jobs can add
-- canonical meter keys without widening a database CHECK constraint.
-- ============================================================================

-- Replace any older meter-name CHECK constraints with the canonical key shape.
ALTER TABLE purser.tier_pricing_rules
    DROP CONSTRAINT IF EXISTS chk_tier_pricing_meter;
ALTER TABLE purser.tier_pricing_rules
    ADD CONSTRAINT chk_tier_pricing_meter
    CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$');

ALTER TABLE purser.subscription_pricing_overrides
    DROP CONSTRAINT IF EXISTS chk_subscription_pricing_meter;
ALTER TABLE purser.subscription_pricing_overrides
    ADD CONSTRAINT chk_subscription_pricing_meter
    CHECK (meter ~ '^[a-z][a-z0-9_]{0,63}$');

-- Durable owner-transaction obligations for recompiling media authority.
CREATE TABLE IF NOT EXISTS purser.media_authority_refresh_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_event_id VARCHAR(255) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    reason VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_purser_media_authority_refresh_status
        CHECK (status IN ('pending', 'delivering', 'completed')),
    CONSTRAINT chk_purser_media_authority_refresh_reason
        CHECK (btrim(reason) <> '')
);

CREATE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_pending
    ON purser.media_authority_refresh_outbox(next_attempt_at, created_at)
    WHERE status <> 'completed';

CREATE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_tenant
    ON purser.media_authority_refresh_outbox(tenant_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_media_authority_refresh_coalesced
    ON purser.media_authority_refresh_outbox(tenant_id, reason)
    WHERE status <> 'completed';

CREATE OR REPLACE FUNCTION purser.enqueue_media_authority_refresh(
    p_tenant_id UUID,
    p_reason TEXT
) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_tenant_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    VALUES (p_reason || ':' || gen_random_uuid()::text, p_tenant_id, p_reason)
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending',
        next_attempt_at = NOW(),
        completed_at = NULL,
        last_error = NULL,
        -- Keep an active delivery lease as a short serialization fence. The
        -- claimed revision can no longer complete this row, and the replacement
        -- revision becomes claimable as soon as that lease ends.
        lease_expires_at = CASE
            WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at
            ELSE NULL
        END,
        updated_at = NOW();
END;
$$;

CREATE OR REPLACE FUNCTION purser.media_authority_subscription_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_authority_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_tenant_subscription_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tier_id, status, billing_model, payment_method,
    stripe_subscription_id, mollie_subscription_id, billing_period_start, billing_period_end
ON purser.tenant_subscriptions
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_subscription_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_prepaid_gate_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
BEGIN
    IF TG_OP = 'UPDATE' AND ((OLD.balance_cents <= 0) = (NEW.balance_cents <= 0)) THEN
        RETURN NEW;
    END IF;
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'prepaid_admission_gate_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_prepaid_balance_media_authority
AFTER INSERT OR DELETE OR UPDATE OF balance_cents
ON purser.prepaid_balances
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_prepaid_gate_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_subscription_entitlement_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_subscription UUID;
    affected_tenant UUID;
BEGIN
    affected_subscription := CASE WHEN TG_OP = 'DELETE' THEN OLD.subscription_id ELSE NEW.subscription_id END;
    SELECT tenant_id INTO affected_tenant
    FROM purser.tenant_subscriptions
    WHERE id = affected_subscription;
    PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'subscription_entitlement_changed');
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_subscription_entitlement_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.subscription_entitlement_overrides
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_subscription_entitlement_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_tier UUID;
    new_tier UUID;
    change_reason TEXT;
BEGIN
    old_tier := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN OLD.tier_id ELSE NULL END;
    new_tier := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN NEW.tier_id ELSE NULL END;
    change_reason := CASE TG_TABLE_NAME
        WHEN 'tier_entitlements' THEN 'tier_entitlement_changed'
        ELSE 'tier_allowance_changed'
    END;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    SELECT change_reason || ':' || gen_random_uuid()::text, affected.tenant_id, change_reason
    FROM (
        SELECT DISTINCT subscriptions.tenant_id
        FROM purser.tenant_subscriptions AS subscriptions
        WHERE subscriptions.tier_id IN (old_tier, new_tier)
          AND subscriptions.status <> 'cancelled'
    ) AS affected
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending', next_attempt_at = NOW(), completed_at = NULL,
        last_error = NULL,
        lease_expires_at = CASE WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at ELSE NULL END,
        updated_at = NOW();
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;
CREATE OR REPLACE TRIGGER trg_tier_entitlement_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.tier_entitlements
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_tier_changed();
CREATE OR REPLACE TRIGGER trg_tier_pricing_media_authority
AFTER INSERT OR UPDATE OR DELETE
ON purser.tier_pricing_rules
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_tier_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_billing_tier_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tier UUID;
BEGIN
    affected_tier := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    INSERT INTO purser.media_authority_refresh_outbox(source_event_id, tenant_id, reason)
    SELECT 'billing_tier_authority_changed:' || gen_random_uuid()::text,
           affected.tenant_id, 'billing_tier_authority_changed'
    FROM (
        SELECT DISTINCT subscriptions.tenant_id
        FROM purser.tenant_subscriptions AS subscriptions
        WHERE subscriptions.tier_id = affected_tier
          AND subscriptions.status <> 'cancelled'
    ) AS affected
    ON CONFLICT (tenant_id, reason) WHERE status <> 'completed'
    DO UPDATE SET
        revision = purser.media_authority_refresh_outbox.revision + 1,
        status = 'pending', next_attempt_at = NOW(), completed_at = NULL,
        last_error = NULL,
        lease_expires_at = CASE WHEN purser.media_authority_refresh_outbox.status = 'delivering'
            THEN purser.media_authority_refresh_outbox.lease_expires_at ELSE NULL END,
        updated_at = NOW();
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_billing_tier_media_authority
AFTER INSERT OR DELETE OR UPDATE OF tier_name, tier_level, is_active, features,
    processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
ON purser.billing_tiers
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_billing_tier_changed();

CREATE OR REPLACE FUNCTION purser.media_authority_usage_changed()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_tenant UUID;
    affected_meter TEXT;
BEGIN
    affected_tenant := CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END;
    affected_meter := CASE WHEN TG_OP = 'DELETE' THEN OLD.usage_type ELSE NEW.usage_type END;
    IF affected_meter = 'delivered_minutes' THEN
        PERFORM purser.enqueue_media_authority_refresh(affected_tenant, 'allowance_usage_changed');
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

CREATE OR REPLACE TRIGGER trg_usage_record_media_authority
AFTER INSERT OR DELETE OR UPDATE OF usage_type, usage_value, value_kind, granularity, period_start, period_end
ON purser.usage_records
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_usage_changed();

CREATE OR REPLACE TRIGGER trg_usage_adjustment_media_authority
AFTER INSERT OR DELETE OR UPDATE OF usage_type, delta_value, status, period_start, period_end
ON purser.usage_adjustments
FOR EACH ROW EXECUTE FUNCTION purser.media_authority_usage_changed();


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
    SELECT 'v0.3.0' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
