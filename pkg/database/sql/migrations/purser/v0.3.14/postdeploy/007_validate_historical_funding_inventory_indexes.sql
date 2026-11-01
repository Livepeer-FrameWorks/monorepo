DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_mollie_unbound_funding_listing')
          AND state.indrelid = 'purser.payment_provider_intents'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_mollie_unbound_funding_listing is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_mollie_unbound_funding_tenant')
          AND state.indrelid = 'purser.payment_provider_intents'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_mollie_unbound_funding_tenant is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
