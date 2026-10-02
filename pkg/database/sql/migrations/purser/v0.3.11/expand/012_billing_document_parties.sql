-- v0.3.11: a billing document states its customer and supplier as they were
-- when it was issued. customer_snapshot is the tenant's billing name, company,
-- address, VAT number and billing email at issue; supplier_snapshot is the
-- supplier identity Purser was configured with at issue. Crypto invoices carry
-- both parties in their own columns, and simplified invoices their supplier.
-- A document without a snapshot renders the current details instead.
ALTER TABLE purser.billing_invoices
    ADD COLUMN IF NOT EXISTS customer_snapshot JSONB,
    ADD COLUMN IF NOT EXISTS supplier_snapshot JSONB;
ALTER TABLE purser.billing_payments
    ADD COLUMN IF NOT EXISTS customer_snapshot JSONB,
    ADD COLUMN IF NOT EXISTS supplier_snapshot JSONB;
ALTER TABLE purser.credit_notes
    ADD COLUMN IF NOT EXISTS customer_snapshot JSONB,
    ADD COLUMN IF NOT EXISTS supplier_snapshot JSONB;
ALTER TABLE purser.simplified_invoices
    ADD COLUMN IF NOT EXISTS customer_snapshot JSONB;

-- The tenant's billing details as a document records them when it is issued.
-- A tenant without a subscription row has empty details, so the document
-- states that it had none rather than reading later ones.
CREATE OR REPLACE FUNCTION purser.billing_customer_snapshot(document_tenant_id UUID)
RETURNS JSONB
LANGUAGE sql
STABLE
AS $$
    SELECT jsonb_build_object(
        'name', COALESCE(subscription.billing_name, ''),
        'company', COALESCE(subscription.billing_company, ''),
        'address', COALESCE(subscription.billing_address, '{}'::jsonb),
        'vat_number', COALESCE(subscription.tax_id, ''),
        'email', COALESCE(subscription.billing_email, '')
    )
    FROM (SELECT 1) AS document
    LEFT JOIN purser.tenant_subscriptions AS subscription ON subscription.tenant_id = document_tenant_id
    LIMIT 1;
$$;
