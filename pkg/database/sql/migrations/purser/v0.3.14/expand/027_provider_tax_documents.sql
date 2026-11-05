CREATE TABLE IF NOT EXISTS purser.provider_billing_documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    provider TEXT NOT NULL CHECK(provider='stripe'),
    provider_account_id TEXT NOT NULL,
    livemode BOOLEAN NOT NULL,
    document_kind TEXT NOT NULL CHECK(document_kind IN ('provider_invoice','provider_credit_note')),
    provider_document_id TEXT NOT NULL,
    original_number TEXT NOT NULL,
    source_invoice_id TEXT,
    status TEXT NOT NULL,
    currency CHAR(3) NOT NULL,
    net_minor BIGINT,
    vat_minor BIGINT,
    gross_minor BIGINT NOT NULL,
    tax_status TEXT NOT NULL CHECK(tax_status IN ('ready','legacy_unknown')),
    tax_groups JSONB NOT NULL,
    supplier_snapshot JSONB NOT NULL,
    customer_snapshot JSONB NOT NULL,
    raw_facts JSONB NOT NULL,
    tax_assessment JSONB NOT NULL DEFAULT '{}'::jsonb,
    original_pdf_url TEXT,
    original_pdf BYTEA,
    pdf_sha256 TEXT,
    issued_at TIMESTAMPTZ NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retention_until TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '10 years',
    UNIQUE(provider,provider_account_id,livemode,document_kind,provider_document_id),
    CONSTRAINT provider_document_tax_math CHECK(tax_status<>'ready' OR (net_minor IS NOT NULL AND vat_minor IS NOT NULL AND net_minor+vat_minor=gross_minor)),
    CONSTRAINT provider_document_pdf_hash CHECK((original_pdf IS NULL AND pdf_sha256 IS NULL) OR (original_pdf IS NOT NULL AND length(pdf_sha256)=64))
);
CREATE INDEX IF NOT EXISTS idx_provider_billing_documents_tenant_issued ON purser.provider_billing_documents(tenant_id,issued_at DESC,id DESC);
CREATE OR REPLACE FUNCTION purser.retain_provider_document_facts()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.tenant_id,NEW.provider,NEW.provider_account_id,NEW.livemode,NEW.document_kind,NEW.provider_document_id,NEW.original_number,NEW.source_invoice_id,NEW.currency,NEW.net_minor,NEW.vat_minor,NEW.gross_minor,NEW.tax_status,NEW.tax_groups,NEW.supplier_snapshot,NEW.customer_snapshot,NEW.raw_facts,NEW.tax_assessment,NEW.issued_at)
 IS DISTINCT FROM ROW(OLD.tenant_id,OLD.provider,OLD.provider_account_id,OLD.livemode,OLD.document_kind,OLD.provider_document_id,OLD.original_number,OLD.source_invoice_id,OLD.currency,OLD.net_minor,OLD.vat_minor,OLD.gross_minor,OLD.tax_status,OLD.tax_groups,OLD.supplier_snapshot,OLD.customer_snapshot,OLD.raw_facts,OLD.tax_assessment,OLD.issued_at)
 THEN RAISE EXCEPTION 'original provider document facts are immutable'; END IF;
 IF OLD.original_pdf IS NOT NULL AND ROW(NEW.original_pdf,NEW.pdf_sha256) IS DISTINCT FROM ROW(OLD.original_pdf,OLD.pdf_sha256)
 THEN RAISE EXCEPTION 'original provider PDF is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER provider_document_facts_guard BEFORE UPDATE ON purser.provider_billing_documents FOR EACH ROW EXECUTE FUNCTION purser.retain_provider_document_facts();
CREATE OR REPLACE TRIGGER provider_document_retention_guard BEFORE DELETE ON purser.provider_billing_documents FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
CREATE TABLE IF NOT EXISTS purser.provider_tax_profiles (
 id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 tenant_id UUID NOT NULL,
 profile_sha256 TEXT NOT NULL CHECK(length(profile_sha256)=64),
 profile JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 retention_until TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '10 years',
 PRIMARY KEY(tenant_id,profile_sha256)
);
CREATE OR REPLACE FUNCTION purser.retain_provider_tax_profile() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'provider tax assessments are immutable'; END;
$$;
CREATE OR REPLACE TRIGGER provider_tax_profile_guard BEFORE UPDATE ON purser.provider_tax_profiles FOR EACH ROW EXECUTE FUNCTION purser.retain_provider_tax_profile();
CREATE OR REPLACE TRIGGER provider_tax_profile_retention_guard BEFORE DELETE ON purser.provider_tax_profiles FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
CREATE INDEX IF NOT EXISTS idx_provider_documents_tenant_id ON purser.provider_billing_documents(tenant_id,id);
CREATE INDEX IF NOT EXISTS idx_provider_tax_profiles_tenant_id ON purser.provider_tax_profiles(tenant_id,id);
