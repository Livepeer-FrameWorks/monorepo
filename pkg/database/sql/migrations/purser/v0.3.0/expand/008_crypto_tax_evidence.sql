-- Keep customer-location signals distinct and persist authoritative VIES
-- validation evidence. A chain/network name is not location evidence.

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS evidence_billing_country VARCHAR(2),
    ADD COLUMN IF NOT EXISTS evidence_status VARCHAR(24) NOT NULL DEFAULT 'missing',
    ADD COLUMN IF NOT EXISTS evidence_conflict BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS accounting_signoff_ref VARCHAR(255);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'purser.simplified_invoices'::regclass
          AND conname = 'chk_simplified_invoice_evidence_status'
    ) THEN
        ALTER TABLE purser.simplified_invoices
            ADD CONSTRAINT chk_simplified_invoice_evidence_status CHECK (
                evidence_status IN ('complete', 'single_source', 'conflict', 'missing')
            ) NOT VALID;
    END IF;
END $$;

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
