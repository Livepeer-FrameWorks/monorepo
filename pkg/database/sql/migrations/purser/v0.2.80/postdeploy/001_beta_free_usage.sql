-- Validate the pricing_source CHECK constraint added NOT VALID in this
-- version's expand step. The change is a pure widening ('beta_free' added to the
-- allowed set), so no existing row can violate it; VALIDATE scans under a
-- SHARE UPDATE EXCLUSIVE lock and is non-blocking for DML.

ALTER TABLE purser.invoice_line_items
    VALIDATE CONSTRAINT chk_invoice_line_items_pricing_source;

-- Backfill the would-have-cost column for invoices that predate it. The
-- contract is gross_metered_amount == metered_amount whenever usage was not
-- waived, but the new column defaulted to 0, so historical rows with real
-- metered charges would report grossMeteredAmount = 0. Set gross = metered for
-- them. The WHERE scope is idempotent and cannot clobber a beta_free invoice
-- (those have metered_amount = 0, so metered_amount <> 0 excludes them).
UPDATE purser.billing_invoices
    SET gross_metered_amount = metered_amount
    WHERE gross_metered_amount = 0 AND metered_amount <> 0;
