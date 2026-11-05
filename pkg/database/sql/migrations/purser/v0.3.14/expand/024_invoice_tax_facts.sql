ALTER TABLE purser.billing_invoices
ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS gross_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS document_net_minor BIGINT,
ADD COLUMN IF NOT EXISTS document_vat_minor BIGINT,
ADD COLUMN IF NOT EXISTS document_gross_minor BIGINT,
ADD COLUMN IF NOT EXISTS tax_status TEXT NOT NULL DEFAULT 'legacy_unknown',
ADD COLUMN IF NOT EXISTS tax_policy_version TEXT,
ADD COLUMN IF NOT EXISTS tax_decision JSONB,
ADD COLUMN IF NOT EXISTS tax_summary JSONB,
ADD COLUMN IF NOT EXISTS tax_point_at TIMESTAMPTZ;
ALTER TABLE purser.invoice_line_items
ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS gross_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS vat_rate_bps INTEGER,
ADD COLUMN IF NOT EXISTS tax_treatment TEXT,
ADD COLUMN IF NOT EXISTS tax_point_at TIMESTAMPTZ;
ALTER TABLE purser.credit_notes
ADD COLUMN IF NOT EXISTS net_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS vat_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS gross_eur_cents BIGINT,
ADD COLUMN IF NOT EXISTS document_net_minor BIGINT,
ADD COLUMN IF NOT EXISTS document_vat_minor BIGINT,
ADD COLUMN IF NOT EXISTS document_gross_minor BIGINT,
ADD COLUMN IF NOT EXISTS tax_policy_version TEXT,
ADD COLUMN IF NOT EXISTS tax_summary JSONB;
ALTER TABLE purser.billing_invoices DROP CONSTRAINT IF EXISTS invoice_tax_amounts;
ALTER TABLE purser.billing_invoices ADD CONSTRAINT invoice_tax_amounts CHECK((
 (tax_status IN ('legacy_unknown','review') AND net_eur_cents IS NULL AND vat_eur_cents IS NULL AND gross_eur_cents IS NULL)
 OR (tax_status IN ('ready','no_supply') AND net_eur_cents>=0 AND vat_eur_cents>=0 AND gross_eur_cents=net_eur_cents+vat_eur_cents
 AND document_net_minor>=0 AND document_vat_minor>=0 AND document_gross_minor=document_net_minor+document_vat_minor
 AND tax_point_at IS NOT NULL AND tax_policy_version IS NOT NULL AND tax_decision IS NOT NULL AND tax_summary IS NOT NULL AND jsonb_typeof(tax_decision)='object' AND jsonb_typeof(tax_summary)='array')) IS TRUE) NOT VALID;
CREATE OR REPLACE FUNCTION purser.retain_issued_invoice_tax()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.tax_status IN ('ready','no_supply') AND
 ROW(NEW.net_eur_cents,NEW.vat_eur_cents,NEW.gross_eur_cents,NEW.document_net_minor,NEW.document_vat_minor,NEW.document_gross_minor,NEW.tax_status,NEW.tax_policy_version,NEW.tax_decision,NEW.tax_summary,NEW.tax_point_at,NEW.customer_snapshot,NEW.supplier_snapshot)
 IS DISTINCT FROM ROW(OLD.net_eur_cents,OLD.vat_eur_cents,OLD.gross_eur_cents,OLD.document_net_minor,OLD.document_vat_minor,OLD.document_gross_minor,OLD.tax_status,OLD.tax_policy_version,OLD.tax_decision,OLD.tax_summary,OLD.tax_point_at,OLD.customer_snapshot,OLD.supplier_snapshot)
 THEN RAISE EXCEPTION 'issued invoice tax facts are immutable; issue a credit note'; END IF;
 RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER billing_invoice_tax_guard BEFORE UPDATE ON purser.billing_invoices FOR EACH ROW EXECUTE FUNCTION purser.retain_issued_invoice_tax();
