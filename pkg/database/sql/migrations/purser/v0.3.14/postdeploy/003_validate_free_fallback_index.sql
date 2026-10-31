DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_tenant_subscriptions_free_fallback')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_tenant_subscriptions_free_fallback is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
