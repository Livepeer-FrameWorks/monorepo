-- Immutable links from exceptional post-confirmation reversals to the customer
-- document they correct.

CREATE SEQUENCE IF NOT EXISTS purser.credit_note_number_seq;

CREATE TABLE IF NOT EXISTS purser.credit_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    credit_note_number VARCHAR(50) NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    source_document_type VARCHAR(32) NOT NULL CHECK (source_document_type IN ('invoice', 'simplified_invoice')),
    source_document_id UUID NOT NULL,
    reversal_reference_type VARCHAR(64) NOT NULL,
    reversal_reference_id VARCHAR(255) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    currency CHAR(3) NOT NULL,
    reason TEXT NOT NULL,
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
);

CREATE INDEX IF NOT EXISTS idx_credit_notes_tenant_issued
    ON purser.credit_notes(tenant_id, issued_at DESC);
