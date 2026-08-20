-- Durable small-balance carry-forward for postpaid provider collection.
-- The balance row is locked and updated in the same transaction as invoice
-- finalization; the per-invoice entry is the immutable audit trail.

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
