DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_checkout_unresolved')
          AND state.indrelid = 'purser.payment_provider_intents'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_checkout_unresolved is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_provider_refund_settlement_payment')
          AND state.indrelid = 'purser.provider_webhook_inbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_provider_refund_settlement_payment is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
