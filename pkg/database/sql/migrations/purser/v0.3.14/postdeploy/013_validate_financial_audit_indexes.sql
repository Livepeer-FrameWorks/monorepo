DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_billing_invoices_financial_audit')
          AND state.indrelid = 'purser.billing_invoices'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_billing_invoices_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_billing_payments_financial_audit')
          AND state.indrelid = 'purser.billing_payments'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_billing_payments_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_simplified_invoices_financial_audit')
          AND state.indrelid = 'purser.simplified_invoices'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_simplified_invoices_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_crypto_invoices_financial_audit')
          AND state.indrelid = 'purser.crypto_invoices'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_crypto_invoices_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_credit_notes_financial_audit')
          AND state.indrelid = 'purser.credit_notes'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_credit_notes_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_settlements_financial_audit')
          AND state.indrelid = 'purser.provider_settlements'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_provider_settlements_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_pending_topups_financial_audit')
          AND state.indrelid = 'purser.pending_topups'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_pending_topups_financial_audit is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_balance_transactions_financial_audit')
          AND state.indrelid = 'purser.balance_transactions'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_balance_transactions_financial_audit is absent, invalid, or not ready';
    END IF;
END;
$$;
