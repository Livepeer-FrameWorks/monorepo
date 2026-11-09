DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM (VALUES
            (to_regclass('purser.idx_provider_tax_assessments_audit'), 'purser.provider_tax_assessments'::regclass),
            (to_regclass('purser.idx_billing_tax_reviews_tenant_id'), 'purser.crypto_accounting_anomalies'::regclass),
            (to_regclass('purser.idx_prepaid_funding_lots_tenant_source_document'), 'purser.prepaid_funding_lots'::regclass),
            (to_regclass('purser.idx_prepaid_funding_lots_active_remaining'), 'purser.prepaid_funding_lots'::regclass),
            (to_regclass('purser.idx_balance_transactions_invoice_credit_key'), 'purser.balance_transactions'::regclass),
            (to_regclass('purser.idx_billing_invoice_phase_close_latest'), 'purser.billing_invoices'::regclass),
            (to_regclass('purser.idx_simplified_invoice_tenant_reference_lower'), 'purser.simplified_invoices'::regclass),
            (to_regclass('purser.idx_crypto_invoice_tenant_reference_lower'), 'purser.crypto_invoices'::regclass),
            (to_regclass('purser.idx_billing_invoice_tenant_reference_lower'), 'purser.billing_invoices'::regclass),
            (to_regclass('purser.idx_provider_usage_credit_allocation'), 'purser.provider_usage_records'::regclass),
            (to_regclass('purser.idx_usage_adjustment_credit_allocation'), 'purser.usage_adjustments'::regclass),
            (to_regclass('purser.idx_cluster_subscription_stripe_owner'), 'purser.cluster_subscriptions'::regclass),
            (to_regclass('purser.idx_tenant_subscription_stripe_owner'), 'purser.tenant_subscriptions'::regclass),
            (to_regclass('purser.idx_provider_intent_stripe_owner'), 'purser.payment_provider_intents'::regclass),
            (to_regclass('purser.idx_billing_payments_confirmed_invoice'), 'purser.billing_payments'::regclass),
            (to_regclass('purser.idx_billing_payments_owner_verification_range'), 'purser.billing_payments'::regclass),
            (to_regclass('purser.idx_billing_invoices_overlap_end'), 'purser.billing_invoices'::regclass),
            (to_regclass('purser.idx_billing_invoices_document_time'), 'purser.billing_invoices'::regclass),
            (to_regclass('purser.idx_billing_invoices_document_unknown_time'), 'purser.billing_invoices'::regclass),
            (to_regclass('purser.idx_billing_payments_document_time'), 'purser.billing_payments'::regclass),
            (to_regclass('purser.idx_billing_payments_document_unknown_time'), 'purser.billing_payments'::regclass),
            (to_regclass('purser.idx_simplified_invoices_document_time'), 'purser.simplified_invoices'::regclass),
            (to_regclass('purser.idx_crypto_invoices_document_time'), 'purser.crypto_invoices'::regclass),
            (to_regclass('purser.idx_credit_notes_document_time'), 'purser.credit_notes'::regclass)
        ) expected(index_id, table_id)
        LEFT JOIN pg_index state ON state.indexrelid = expected.index_id
        WHERE state.indexrelid IS NULL OR state.indrelid <> expected.table_id
           OR NOT (state.indisvalid AND state.indisready)
    ) THEN
        RAISE EXCEPTION 'billing query index is absent, invalid, not ready, or belongs to another table';
    END IF;
END;
$$;
