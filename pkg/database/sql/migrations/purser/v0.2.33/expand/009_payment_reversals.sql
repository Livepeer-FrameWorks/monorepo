-- Refunds, disputes, chargebacks, and manual reversal entries. Each
-- reversal points at the original payment (when one exists), the affected
-- invoice, the balance_transactions row created for the reversal, and the
-- operator_credit_ledger clawback row (if any). operator_review_required
-- defaults FALSE; the prepaid path flips it TRUE for refunds that would
-- drop the prepaid balance below zero so ops can decide whether to
-- recollect or write off. Idempotent on (provider, provider_reversal_id).
-- Schema source of truth: pkg/database/sql/schema/purser.sql

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
