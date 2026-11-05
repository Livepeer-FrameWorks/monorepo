ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS tax_assessment JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE purser.simplified_invoices
    DROP CONSTRAINT IF EXISTS chk_simplified_invoices_tax_assessment;
ALTER TABLE purser.simplified_invoices
    ADD CONSTRAINT chk_simplified_invoices_tax_assessment CHECK (jsonb_typeof(tax_assessment) = 'object') NOT VALID;

ALTER TABLE purser.crypto_invoices
    ADD COLUMN IF NOT EXISTS tax_assessment JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE purser.crypto_invoices
    DROP CONSTRAINT IF EXISTS chk_crypto_invoices_tax_assessment;
ALTER TABLE purser.crypto_invoices
    ADD CONSTRAINT chk_crypto_invoices_tax_assessment CHECK (jsonb_typeof(tax_assessment) = 'object') NOT VALID;

CREATE OR REPLACE FUNCTION purser.prevent_crypto_tax_assessment_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.tax_assessment IS DISTINCT FROM OLD.tax_assessment THEN
        RAISE EXCEPTION 'issued crypto tax assessment is immutable; issue a correction document';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER simplified_invoices_tax_assessment_guard
    BEFORE UPDATE ON purser.simplified_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_crypto_tax_assessment_change();
CREATE OR REPLACE TRIGGER crypto_invoices_tax_assessment_guard
    BEFORE UPDATE ON purser.crypto_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_crypto_tax_assessment_change();
