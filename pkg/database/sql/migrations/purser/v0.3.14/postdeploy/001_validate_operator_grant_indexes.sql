DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_index state
        WHERE state.indexrelid=to_regclass('purser.idx_operator_grants_expiry')
          AND state.indrelid='purser.subscription_operator_grants'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])') THEN
        RAISE EXCEPTION 'idx_operator_grants_expiry is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_index state
        WHERE state.indexrelid=to_regclass('purser.idx_operator_grants_listing')
          AND state.indrelid='purser.subscription_operator_grants'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])') THEN
        RAISE EXCEPTION 'idx_operator_grants_listing is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_index state
        WHERE state.indexrelid=to_regclass('purser.idx_operator_grants_expiry_retry')
          AND state.indrelid='purser.subscription_operator_grants'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])') THEN
        RAISE EXCEPTION 'idx_operator_grants_expiry_retry is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
