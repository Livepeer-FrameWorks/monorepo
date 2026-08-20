-- Freeze the tax-document decision and customer evidence before a crypto
-- payment is accepted. Full customer invoices are stored separately from the
-- anonymous simplified-invoice register.

ALTER TABLE purser.crypto_wallets
    ADD COLUMN IF NOT EXISTS tax_document_kind VARCHAR(16) NOT NULL DEFAULT 'simplified',
    ADD COLUMN IF NOT EXISTS tax_profile_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE purser.x402_payment_quotes
    ADD COLUMN IF NOT EXISTS tax_document_kind VARCHAR(16) NOT NULL DEFAULT 'simplified',
    ADD COLUMN IF NOT EXISTS tax_profile_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE purser.tenant_subscriptions
    ADD COLUMN IF NOT EXISTS billing_name VARCHAR(255);

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS supplier_registration_number VARCHAR(100),
    ADD COLUMN IF NOT EXISTS service_description TEXT,
    ADD COLUMN IF NOT EXISTS service_quantity INTEGER,
    ADD COLUMN IF NOT EXISTS service_date DATE;

ALTER TABLE purser.simplified_invoices
    DROP CONSTRAINT IF EXISTS chk_simplified_invoice_service_quantity,
    ADD CONSTRAINT chk_simplified_invoice_service_quantity CHECK (service_quantity > 0) NOT VALID;

ALTER TABLE purser.simplified_invoices
    DROP CONSTRAINT IF EXISTS chk_simplified_invoice_evidence_status,
    ADD CONSTRAINT chk_simplified_invoice_evidence_status
    CHECK (evidence_status IN ('complete', 'single_source', 'conflict', 'missing')) NOT VALID;

ALTER TABLE purser.crypto_wallets
    DROP CONSTRAINT IF EXISTS crypto_wallets_tax_document_kind_check;
ALTER TABLE purser.crypto_wallets
    ADD CONSTRAINT crypto_wallets_tax_document_kind_check
    CHECK (tax_document_kind IN ('simplified', 'full')) NOT VALID;

ALTER TABLE purser.x402_payment_quotes
    DROP CONSTRAINT IF EXISTS x402_payment_quotes_tax_document_kind_check;
ALTER TABLE purser.x402_payment_quotes
    ADD CONSTRAINT x402_payment_quotes_tax_document_kind_check
    CHECK (tax_document_kind IN ('simplified', 'full')) NOT VALID;

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
    evidence_status VARCHAR(24) NOT NULL,
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

ALTER TABLE purser.crypto_invoices
    DROP CONSTRAINT IF EXISTS chk_crypto_invoice_evidence_status;
ALTER TABLE purser.crypto_invoices
    ADD CONSTRAINT chk_crypto_invoice_evidence_status
    CHECK (evidence_status IN ('complete', 'single_source', 'conflict', 'missing')) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_crypto_invoices_tenant
    ON purser.crypto_invoices(tenant_id, issued_at DESC);
CREATE INDEX IF NOT EXISTS idx_crypto_invoices_reference
    ON purser.crypto_invoices(reference_type, reference_id);

DROP TRIGGER IF EXISTS crypto_invoices_retention_guard ON purser.crypto_invoices;
CREATE TRIGGER crypto_invoices_retention_guard
    BEFORE DELETE ON purser.crypto_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

ALTER TABLE purser.credit_notes
    DROP CONSTRAINT IF EXISTS credit_notes_source_document_type_check;
ALTER TABLE purser.credit_notes
    ADD CONSTRAINT credit_notes_source_document_type_check
    CHECK (source_document_type IN ('invoice', 'simplified_invoice', 'crypto_invoice')) NOT VALID;
