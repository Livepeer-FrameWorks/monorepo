-- Beta free usage: while billing/metering is being validated, metered usage
-- rates to €0 (WAIVE_USAGE_CHARGES) while the monthly subscription still
-- charges. Two additive changes:
--
--   1. gross_metered_amount on billing_invoices preserves the would-have-cost
--      usage total for display. DECIMAL(20,2) — wider than the other money
--      columns — so a metering bug producing garbage usage cannot overflow the
--      column and abort invoice generation. Equals metered_amount when the flag
--      is off; never charged.
--
--   2. 'beta_free' added to the invoice_line_items pricing_source enum so waived
--      usage lines are distinct from permanently-free clusters ('free_unmetered').
--      ADD CONSTRAINT runs NOT VALID so the swap is non-blocking; this version's
--      postdeploy step runs VALIDATE CONSTRAINT. The change is a pure widening,
--      so no existing row can violate it.
--
-- Expand-only and idempotent. Ending the beta needs no rollback: the column
-- simply tracks metered_amount again once the flag is off.

ALTER TABLE purser.billing_invoices
  ADD COLUMN IF NOT EXISTS gross_metered_amount DECIMAL(20,2) NOT NULL DEFAULT 0;

ALTER TABLE purser.invoice_line_items
  DROP CONSTRAINT IF EXISTS chk_invoice_line_items_pricing_source;
ALTER TABLE purser.invoice_line_items
  ADD CONSTRAINT chk_invoice_line_items_pricing_source
  CHECK (pricing_source IN (
    'tier', 'cluster_metered', 'cluster_monthly', 'cluster_custom',
    'free_unmetered', 'self_hosted', 'included_subscription', 'beta_free'
  )) NOT VALID;
