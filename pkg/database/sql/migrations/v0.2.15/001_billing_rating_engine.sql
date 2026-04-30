-- Billing rating engine: split allocation/overage JSONBs into normalized
-- pricing rules + entitlements, and add invoice line items.
--
-- The schema file (pkg/database/sql/schema/purser.sql) carries the canonical
-- CREATE for fresh DBs. This migration applies the same shape to existing
-- Purser DBs and is a no-op on every other database in postgres_databases
-- because the body is gated on `purser` schema existence.
--
-- Tier pricing rules, entitlements, and subscription overrides are copied from
-- the source JSONB columns before those columns are dropped. Invoice line items
-- are written by the rating engine on draft refresh and monthly finalization.

DO $$
DECLARE
    v_tier RECORD;
    v_sub  RECORD;
    v_storage_unit TEXT;
    v_storage_limit NUMERIC;
    v_storage_price NUMERIC;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'purser') THEN
        RETURN;
    END IF;

    ALTER TABLE purser.billing_invoices
        ALTER COLUMN currency SET DEFAULT 'EUR';
    ALTER TABLE purser.billing_payments
        ALTER COLUMN currency SET DEFAULT 'EUR';
    ALTER TABLE purser.prepaid_balances
        ALTER COLUMN currency SET DEFAULT 'EUR';
    ALTER TABLE purser.pending_topups
        ALTER COLUMN currency SET DEFAULT 'EUR';

    UPDATE purser.billing_payments
        SET method = 'card'
        WHERE method IN ('stripe', 'mollie', 'directdebit', 'creditcard', 'ideal');
    ALTER TABLE purser.billing_payments
        ADD COLUMN IF NOT EXISTS payment_url TEXT;
    WITH ranked AS (
        SELECT id,
               ROW_NUMBER() OVER (PARTITION BY invoice_id, method ORDER BY created_at DESC, id DESC) AS rn
        FROM purser.billing_payments
        WHERE status = 'pending'
    )
    UPDATE purser.billing_payments p
        SET status = 'failed', updated_at = NOW()
    FROM ranked r
    WHERE p.id = r.id AND r.rn > 1;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_payments_pending_invoice_method
        ON purser.billing_payments(invoice_id, method)
        WHERE status = 'pending';
    ALTER TABLE purser.billing_payments
        DROP CONSTRAINT IF EXISTS chk_billing_payments_method;
    ALTER TABLE purser.billing_payments
        ADD CONSTRAINT chk_billing_payments_method
        CHECK (method IN ('card', 'crypto_eth', 'crypto_usdc', 'bank_transfer'));

    ALTER TABLE IF EXISTS purser.crypto_wallets
        DROP CONSTRAINT IF EXISTS crypto_wallets_invoice_id_asset_key;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_crypto_wallets_active_invoice_asset
        ON purser.crypto_wallets(invoice_id, asset)
        WHERE purpose = 'invoice' AND status IN ('pending', 'confirming');

    -- Create normalized tables before copying data from source JSONB columns.
    CREATE TABLE IF NOT EXISTS purser.tier_entitlements (
        tier_id UUID NOT NULL REFERENCES purser.billing_tiers(id) ON DELETE CASCADE,
        key VARCHAR(64) NOT NULL,
        value JSONB NOT NULL,
        PRIMARY KEY (tier_id, key)
    );

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
    CREATE INDEX IF NOT EXISTS idx_tier_pricing_rules_tier
        ON purser.tier_pricing_rules(tier_id);

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

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_tier_pricing_meter'
          AND conrelid = 'purser.tier_pricing_rules'::regclass
    ) THEN
        ALTER TABLE purser.tier_pricing_rules
            ADD CONSTRAINT chk_tier_pricing_meter
            CHECK (meter IN ('delivered_minutes', 'average_storage_gb', 'ai_gpu_hours', 'processing_seconds'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_tier_pricing_model'
          AND conrelid = 'purser.tier_pricing_rules'::regclass
    ) THEN
        ALTER TABLE purser.tier_pricing_rules
            ADD CONSTRAINT chk_tier_pricing_model
            CHECK (model IN ('tiered_graduated', 'all_usage', 'codec_multiplier'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_subscription_pricing_meter'
          AND conrelid = 'purser.subscription_pricing_overrides'::regclass
    ) THEN
        ALTER TABLE purser.subscription_pricing_overrides
            ADD CONSTRAINT chk_subscription_pricing_meter
            CHECK (meter IN ('delivered_minutes', 'average_storage_gb', 'ai_gpu_hours', 'processing_seconds'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_subscription_pricing_model'
          AND conrelid = 'purser.subscription_pricing_overrides'::regclass
    ) THEN
        ALTER TABLE purser.subscription_pricing_overrides
            ADD CONSTRAINT chk_subscription_pricing_model
            CHECK (model IS NULL OR model IN ('tiered_graduated', 'all_usage', 'codec_multiplier'));
    END IF;

    CREATE TABLE IF NOT EXISTS purser.invoice_line_items (
        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        invoice_id UUID NOT NULL REFERENCES purser.billing_invoices(id) ON DELETE CASCADE,
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
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        UNIQUE (invoice_id, line_key)
    );
    -- tenant_id is mandatory on line items because invoice and audit reads
    -- enforce tenant filters. Existing rows are backfilled from the parent
    -- invoice before the column is enforced.
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'invoice_line_items'
          AND column_name = 'tenant_id'
    ) THEN
        ALTER TABLE purser.invoice_line_items ADD COLUMN tenant_id UUID;
    END IF;
    UPDATE purser.invoice_line_items li
        SET tenant_id = bi.tenant_id
        FROM purser.billing_invoices bi
        WHERE li.invoice_id = bi.id AND li.tenant_id IS NULL;
    ALTER TABLE purser.invoice_line_items ALTER COLUMN tenant_id SET NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_invoice_line_items_invoice
        ON purser.invoice_line_items(invoice_id);
    CREATE INDEX IF NOT EXISTS idx_invoice_line_items_tenant
        ON purser.invoice_line_items(tenant_id);

    CREATE UNIQUE INDEX IF NOT EXISTS idx_purser_billing_invoices_tenant_period_unique
        ON purser.billing_invoices(tenant_id, period_start)
        WHERE period_start IS NOT NULL;

    -- Sub-cent residual carried across prepaid deductions so per-event usage
    -- under one cent doesn't get truncated to zero. Unit: 10^-8 of a currency
    -- unit (i.e. micro-cents).
    ALTER TABLE purser.prepaid_balances
        ADD COLUMN IF NOT EXISTS balance_remainder_micro BIGINT NOT NULL DEFAULT 0;

    -- Backfill tier pricing rules and entitlements from source JSONB columns
    -- only when those columns still exist (idempotent rerun).
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'billing_tiers'
          AND column_name = 'overage_rates'
    ) THEN
        FOR v_tier IN EXECUTE
            'SELECT id, currency, bandwidth_allocation, storage_allocation, compute_allocation, overage_rates
               FROM purser.billing_tiers'
        LOOP
            -- delivered_minutes: included = bandwidth_allocation.limit;
            -- unit price preferred from overage_rates.bandwidth, falling back
            -- to the allocation row.
            IF v_tier.bandwidth_allocation IS NOT NULL AND v_tier.bandwidth_allocation ? 'limit' THEN
                INSERT INTO purser.tier_pricing_rules
                    (tier_id, meter, model, currency, included_quantity, unit_price, config)
                VALUES (
                    v_tier.id, 'delivered_minutes', 'tiered_graduated', v_tier.currency,
                    COALESCE((v_tier.bandwidth_allocation ->> 'limit')::NUMERIC, 0),
                    COALESCE((v_tier.overage_rates -> 'bandwidth' ->> 'unit_price')::NUMERIC,
                             (v_tier.bandwidth_allocation ->> 'unit_price')::NUMERIC, 0),
                    '{}'::JSONB
                )
                ON CONFLICT (tier_id, meter) DO NOTHING;
            END IF;

            -- storage: split based on the source unit. unit='retention_days' was
            -- a lifecycle policy, NOT a billing allowance; promote it to an
            -- entitlement and bill all GB at the storage overage rate.
            -- unit='gb' was a real GB allowance; bill (qty - limit) at the rate.
            v_storage_unit := COALESCE(v_tier.storage_allocation ->> 'unit', '');
            v_storage_limit := COALESCE((v_tier.storage_allocation ->> 'limit')::NUMERIC, 0);
            v_storage_price := COALESCE(
                (v_tier.overage_rates -> 'storage' ->> 'unit_price')::NUMERIC,
                (v_tier.storage_allocation ->> 'unit_price')::NUMERIC,
                0
            );

            IF v_storage_unit = 'retention_days' AND v_storage_limit > 0 THEN
                INSERT INTO purser.tier_entitlements (tier_id, key, value)
                VALUES (v_tier.id, 'recording_retention_days', to_jsonb(v_storage_limit::INT))
                ON CONFLICT (tier_id, key) DO NOTHING;

                INSERT INTO purser.tier_pricing_rules
                    (tier_id, meter, model, currency, included_quantity, unit_price, config)
                VALUES (
                    v_tier.id, 'average_storage_gb', 'all_usage', v_tier.currency,
                    0, v_storage_price, '{}'::JSONB
                )
                ON CONFLICT (tier_id, meter) DO NOTHING;
            ELSE
                INSERT INTO purser.tier_pricing_rules
                    (tier_id, meter, model, currency, included_quantity, unit_price, config)
                VALUES (
                    v_tier.id, 'average_storage_gb',
                    CASE WHEN v_storage_limit > 0 THEN 'tiered_graduated' ELSE 'all_usage' END,
                    v_tier.currency, v_storage_limit, v_storage_price, '{}'::JSONB
                )
                ON CONFLICT (tier_id, meter) DO NOTHING;
            END IF;

            -- ai_gpu_hours: included = compute_allocation.limit;
            -- unit price preferred from overage_rates.compute.
            IF v_tier.compute_allocation IS NOT NULL THEN
                INSERT INTO purser.tier_pricing_rules
                    (tier_id, meter, model, currency, included_quantity, unit_price, config)
                VALUES (
                    v_tier.id, 'ai_gpu_hours', 'tiered_graduated', v_tier.currency,
                    COALESCE((v_tier.compute_allocation ->> 'limit')::NUMERIC, 0),
                    COALESCE((v_tier.overage_rates -> 'compute' ->> 'unit_price')::NUMERIC,
                             (v_tier.compute_allocation ->> 'unit_price')::NUMERIC, 0),
                    '{}'::JSONB
                )
                ON CONFLICT (tier_id, meter) DO NOTHING;
            END IF;

            -- processing_seconds: codec_multiplier model. Stored as a per-minute
            -- H264 base rate (h264_rate_per_min) plus per-codec multipliers in
            -- config. Only emit when the source has a non-zero rate, since most
            -- source tiers had h264_rate_per_min: 0 (no processing billing).
            IF v_tier.overage_rates -> 'processing' IS NOT NULL
               AND COALESCE((v_tier.overage_rates -> 'processing' ->> 'h264_rate_per_min')::NUMERIC, 0) > 0 THEN
                INSERT INTO purser.tier_pricing_rules
                    (tier_id, meter, model, currency, included_quantity, unit_price, config)
                VALUES (
                    v_tier.id, 'processing_seconds', 'codec_multiplier', v_tier.currency,
                    0,
                    (v_tier.overage_rates -> 'processing' ->> 'h264_rate_per_min')::NUMERIC,
                    jsonb_build_object(
                        'codec_multipliers',
                        COALESCE(v_tier.overage_rates -> 'processing' -> 'codec_multipliers', '{}'::JSONB)
                    )
                )
                ON CONFLICT (tier_id, meter) DO NOTHING;
            END IF;
        END LOOP;
    END IF;

    -- Backfill subscription pricing overrides from custom_pricing.overage_rates
    -- and custom_allocations.
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'purser' AND table_name = 'tenant_subscriptions'
          AND column_name = 'custom_pricing'
    ) THEN
        FOR v_sub IN EXECUTE
            'SELECT id, custom_pricing, custom_allocations
               FROM purser.tenant_subscriptions
               WHERE COALESCE(custom_pricing, ''{}''::JSONB) <> ''{}''::JSONB
                  OR COALESCE(custom_allocations, ''{}''::JSONB) <> ''{}''::JSONB'
        LOOP
            -- custom_allocations override delivered_minutes. Only fields present
            -- in the source row become overrides; missing unit_price stays NULL so
            -- the resolver falls back to the
            -- tier rule's price instead of treating overage as free.
            IF v_sub.custom_allocations IS NOT NULL AND v_sub.custom_allocations ? 'limit' THEN
                INSERT INTO purser.subscription_pricing_overrides
                    (subscription_id, meter, model, included_quantity, unit_price)
                VALUES (
                    v_sub.id, 'delivered_minutes', 'tiered_graduated',
                    (v_sub.custom_allocations ->> 'limit')::NUMERIC,
                    NULLIF(v_sub.custom_allocations ->> 'unit_price', '')::NUMERIC
                )
                ON CONFLICT (subscription_id, meter) DO NOTHING;
            END IF;

            IF v_sub.custom_pricing -> 'overage_rates' -> 'bandwidth' ->> 'unit_price' IS NOT NULL THEN
                INSERT INTO purser.subscription_pricing_overrides
                    (subscription_id, meter, unit_price)
                VALUES (v_sub.id, 'delivered_minutes',
                    (v_sub.custom_pricing -> 'overage_rates' -> 'bandwidth' ->> 'unit_price')::NUMERIC)
                ON CONFLICT (subscription_id, meter) DO UPDATE SET unit_price = EXCLUDED.unit_price;
            END IF;
            IF v_sub.custom_pricing -> 'overage_rates' -> 'storage' ->> 'unit_price' IS NOT NULL THEN
                INSERT INTO purser.subscription_pricing_overrides
                    (subscription_id, meter, unit_price)
                VALUES (v_sub.id, 'average_storage_gb',
                    (v_sub.custom_pricing -> 'overage_rates' -> 'storage' ->> 'unit_price')::NUMERIC)
                ON CONFLICT (subscription_id, meter) DO UPDATE SET unit_price = EXCLUDED.unit_price;
            END IF;
            IF v_sub.custom_pricing -> 'overage_rates' -> 'compute' ->> 'unit_price' IS NOT NULL THEN
                INSERT INTO purser.subscription_pricing_overrides
                    (subscription_id, meter, unit_price)
                VALUES (v_sub.id, 'ai_gpu_hours',
                    (v_sub.custom_pricing -> 'overage_rates' -> 'compute' ->> 'unit_price')::NUMERIC)
                ON CONFLICT (subscription_id, meter) DO UPDATE SET unit_price = EXCLUDED.unit_price;
            END IF;
        END LOOP;
    END IF;

    -- Drop source columns after the normalized catalog and override tables are populated.
    ALTER TABLE purser.billing_tiers DROP COLUMN IF EXISTS bandwidth_allocation;
    ALTER TABLE purser.billing_tiers DROP COLUMN IF EXISTS storage_allocation;
    ALTER TABLE purser.billing_tiers DROP COLUMN IF EXISTS compute_allocation;
    ALTER TABLE purser.billing_tiers DROP COLUMN IF EXISTS overage_rates;

    ALTER TABLE purser.tenant_subscriptions DROP COLUMN IF EXISTS custom_pricing;
    ALTER TABLE purser.tenant_subscriptions DROP COLUMN IF EXISTS custom_allocations;
END
$$;
