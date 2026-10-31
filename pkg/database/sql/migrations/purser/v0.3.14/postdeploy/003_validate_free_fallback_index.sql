DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_tenant_subscriptions_free_fallback')
          AND state.indrelid = 'purser.tenant_subscriptions'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
          AND pg_get_indexdef(state.indexrelid, 1, true) IN ('free_fallback_at', 'free_fallback_at ASC')
          AND pg_get_expr(state.indpred, state.indrelid) ~ '^[(]?free_fallback_at IS NOT NULL[)]?$'
    ) THEN
        RAISE EXCEPTION 'idx_tenant_subscriptions_free_fallback is absent, invalid, hash-sharded, or has a different key/predicate';
    END IF;
END;
$$;
