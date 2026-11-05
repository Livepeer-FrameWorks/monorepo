ALTER TABLE purser.billing_payments ADD COLUMN IF NOT EXISTS tenant_id UUID;

CREATE OR REPLACE FUNCTION purser.set_payment_tenant()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE owner UUID;
BEGIN
    SELECT tenant_id INTO STRICT owner FROM purser.billing_invoices WHERE id=NEW.invoice_id;
    IF NEW.tenant_id IS NOT NULL AND NEW.tenant_id IS DISTINCT FROM owner THEN
        RAISE EXCEPTION 'payment tenant does not own the invoice';
    END IF;
    NEW.tenant_id := owner;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER billing_payment_tenant_guard
BEFORE INSERT OR UPDATE ON purser.billing_payments
FOR EACH ROW EXECUTE FUNCTION purser.set_payment_tenant();

ALTER TABLE purser.billing_payments DROP CONSTRAINT IF EXISTS billing_payment_tenant_required;
ALTER TABLE purser.billing_payments ADD CONSTRAINT billing_payment_tenant_required CHECK(tenant_id IS NOT NULL) NOT VALID;
