-- Preserve the prior evidence reference during the transition to an explicit
-- top-up tax-policy version. New writes populate tax_policy_ref directly.

UPDATE purser.simplified_invoices
SET tax_policy_ref = COALESCE(tax_policy_ref, accounting_signoff_ref)
WHERE tax_policy_ref IS NULL;
