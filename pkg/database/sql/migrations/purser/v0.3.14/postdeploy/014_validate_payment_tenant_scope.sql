ALTER TABLE purser.billing_payments VALIDATE CONSTRAINT billing_payment_tenant_required;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_billing_payments_tenant_id_uuid')
          AND state.indrelid = 'purser.billing_payments'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_billing_payments_tenant_id_uuid is absent, invalid, or not ready';
    END IF;
END $$;
