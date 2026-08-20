-- Immutable customer-facing billing document retention and download support.
-- Ten years is an operational floor pending deployment-specific accounting
-- approval; records may be retained longer by backup/legal-hold policy.

CREATE SEQUENCE IF NOT EXISTS purser.billing_invoice_number_seq;
ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS invoice_number VARCHAR(50);
ALTER TABLE purser.billing_invoices
    ALTER COLUMN invoice_number SET DEFAULT (
        'INV-' || LPAD(nextval('purser.billing_invoice_number_seq')::text, 10, '0')
    );
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_invoices_number
    ON purser.billing_invoices(invoice_number);

ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS retention_until TIMESTAMPTZ;
ALTER TABLE purser.billing_invoices
    ALTER COLUMN retention_until SET DEFAULT (NOW() + INTERVAL '10 years');

ALTER TABLE purser.billing_payments
    ADD COLUMN IF NOT EXISTS retention_until TIMESTAMPTZ;
ALTER TABLE purser.billing_payments
    ALTER COLUMN retention_until SET DEFAULT (NOW() + INTERVAL '10 years');

ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS retention_until TIMESTAMPTZ;
ALTER TABLE purser.simplified_invoices
    ALTER COLUMN retention_until SET DEFAULT (NOW() + INTERVAL '10 years');

ALTER TABLE purser.credit_notes
    ADD COLUMN IF NOT EXISTS retention_until TIMESTAMPTZ;
ALTER TABLE purser.credit_notes
    ALTER COLUMN retention_until SET DEFAULT (NOW() + INTERVAL '10 years');

CREATE OR REPLACE FUNCTION purser.prevent_billing_document_early_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.retention_until > NOW() THEN
        RAISE EXCEPTION 'billing document % is retained until %', OLD.id, OLD.retention_until;
    END IF;
    RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS billing_invoices_retention_guard ON purser.billing_invoices;
CREATE TRIGGER billing_invoices_retention_guard
    BEFORE DELETE ON purser.billing_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

DROP TRIGGER IF EXISTS billing_payments_retention_guard ON purser.billing_payments;
CREATE TRIGGER billing_payments_retention_guard
    BEFORE DELETE ON purser.billing_payments
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

DROP TRIGGER IF EXISTS simplified_invoices_retention_guard ON purser.simplified_invoices;
CREATE TRIGGER simplified_invoices_retention_guard
    BEFORE DELETE ON purser.simplified_invoices
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();

DROP TRIGGER IF EXISTS credit_notes_retention_guard ON purser.credit_notes;
CREATE TRIGGER credit_notes_retention_guard
    BEFORE DELETE ON purser.credit_notes
    FOR EACH ROW EXECUTE FUNCTION purser.prevent_billing_document_early_delete();
