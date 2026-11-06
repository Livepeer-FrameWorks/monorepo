ALTER TABLE purser.provider_billing_documents
 ADD COLUMN IF NOT EXISTS reviewed_tax_status TEXT,
 ADD COLUMN IF NOT EXISTS reviewed_tax_assessment JSONB,
 ADD COLUMN IF NOT EXISTS tax_reviewed_at TIMESTAMPTZ;

ALTER TABLE purser.provider_billing_documents DROP CONSTRAINT IF EXISTS provider_document_review_shape;
ALTER TABLE purser.provider_billing_documents ADD CONSTRAINT provider_document_review_shape CHECK (
 (reviewed_tax_status IS NULL AND reviewed_tax_assessment IS NULL)
 OR (reviewed_tax_status IS NOT NULL AND reviewed_tax_status IN ('ready','legacy_unknown') AND reviewed_tax_assessment IS NOT NULL
     AND jsonb_typeof(reviewed_tax_assessment)='object' AND tax_reviewed_at IS NOT NULL)
) NOT VALID;
ALTER TABLE purser.provider_billing_documents DROP CONSTRAINT IF EXISTS provider_document_review_math;
ALTER TABLE purser.provider_billing_documents ADD CONSTRAINT provider_document_review_math CHECK (
 reviewed_tax_status IS DISTINCT FROM 'ready'
 OR (net_minor IS NOT NULL AND vat_minor IS NOT NULL AND net_minor+vat_minor=gross_minor)
) NOT VALID;

CREATE OR REPLACE FUNCTION purser.retain_verified_provider_review() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.tax_status='ready' OR OLD.reviewed_tax_status='ready') AND ROW(NEW.reviewed_tax_status,NEW.reviewed_tax_assessment,NEW.tax_reviewed_at)
 IS DISTINCT FROM ROW(OLD.reviewed_tax_status,OLD.reviewed_tax_assessment,OLD.tax_reviewed_at)
 THEN RAISE EXCEPTION 'verified provider tax review is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER provider_document_review_guard BEFORE UPDATE ON purser.provider_billing_documents
FOR EACH ROW EXECUTE FUNCTION purser.retain_verified_provider_review();
