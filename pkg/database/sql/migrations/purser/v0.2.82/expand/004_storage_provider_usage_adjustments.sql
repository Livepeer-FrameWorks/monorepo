CREATE TABLE IF NOT EXISTS purser.storage_provider_usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    usage_tenant_id UUID NOT NULL,
    customer_cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    storage_provider_tenant_id VARCHAR(100) NOT NULL DEFAULT '',
    storage_provider_cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    storage_backend VARCHAR(32) NOT NULL DEFAULT 'unknown',
    storage_scope VARCHAR(20) NOT NULL,
    usage_type VARCHAR(64) NOT NULL,
    gb_seconds DECIMAL(20,6) NOT NULL DEFAULT 0,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    granularity VARCHAR(20) NOT NULL DEFAULT 'minute_5',
    value_kind VARCHAR(20) NOT NULL DEFAULT 'delta',
    source VARCHAR(64) NOT NULL DEFAULT 'kafka',
    usage_details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (
        usage_tenant_id, customer_cluster_id,
        storage_provider_tenant_id, storage_provider_cluster_id,
        storage_backend, storage_scope, usage_type,
        period_start, period_end
    )
);

CREATE INDEX IF NOT EXISTS idx_storage_provider_usage_provider_period
    ON purser.storage_provider_usage_records(storage_provider_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_storage_provider_usage_tenant_period
    ON purser.storage_provider_usage_records(usage_tenant_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_storage_provider_usage_provider_cluster
    ON purser.storage_provider_usage_records(storage_provider_cluster_id, storage_backend, period_start);

ALTER TABLE purser.operator_credit_ledger
    ADD COLUMN IF NOT EXISTS storage_provider_usage_record_id UUID;
ALTER TABLE purser.operator_credit_ledger
    ADD COLUMN IF NOT EXISTS usage_adjustment_id UUID;
ALTER TABLE purser.operator_credit_ledger
    DROP CONSTRAINT IF EXISTS chk_op_credit_source;
ALTER TABLE purser.operator_credit_ledger
    ADD CONSTRAINT chk_op_credit_source CHECK (
        (source_type = 'invoice_line'            AND invoice_line_item_id IS NOT NULL) OR
        (source_type = 'storage_provider_usage'  AND storage_provider_usage_record_id IS NOT NULL) OR
        (source_type = 'usage_adjustment'        AND usage_adjustment_id IS NOT NULL) OR
        (source_type = 'stripe_subscription'     AND stripe_invoice_id    IS NOT NULL)
    ) NOT VALID;
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_storage_provider_usage
    ON purser.operator_credit_ledger(storage_provider_usage_record_id)
    WHERE entry_type = 'accrual' AND source_type = 'storage_provider_usage';
CREATE UNIQUE INDEX IF NOT EXISTS uq_op_credit_accrual_usage_adjustment
    ON purser.operator_credit_ledger(usage_adjustment_id)
    WHERE entry_type = 'accrual' AND source_type = 'usage_adjustment';

CREATE TABLE IF NOT EXISTS purser.usage_adjustments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
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
    UNIQUE (source_system, source_id)
);

ALTER TABLE purser.usage_adjustments
    ADD CONSTRAINT chk_usage_adjustments_status
    CHECK (status IN ('applied', 'ignored', 'pending')) NOT VALID;
ALTER TABLE purser.usage_adjustments
    ADD CONSTRAINT chk_usage_adjustments_value_kind
    CHECK (value_kind = 'correction_delta') NOT VALID;

CREATE INDEX IF NOT EXISTS idx_usage_adjustments_invoice_lookup
    ON purser.usage_adjustments(tenant_id, period_start, period_end, status);
CREATE INDEX IF NOT EXISTS idx_usage_adjustments_source
    ON purser.usage_adjustments(source_system, source_id);
