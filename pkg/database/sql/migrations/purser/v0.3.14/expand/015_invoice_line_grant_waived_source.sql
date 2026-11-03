-- grant_waived marks usage an operator grant waived, apart from the platform
-- beta waiver (beta_free).
ALTER TABLE purser.invoice_line_items
    DROP CONSTRAINT IF EXISTS chk_invoice_line_items_pricing_source;
ALTER TABLE purser.invoice_line_items
    ADD CONSTRAINT chk_invoice_line_items_pricing_source CHECK (pricing_source IN (
        'tier', 'cluster_metered', 'cluster_monthly', 'cluster_custom',
        'free_unmetered', 'self_hosted', 'included_subscription', 'beta_free', 'grant_waived'
    )) NOT VALID;
