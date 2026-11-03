DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_operator_grant_notices_unsent_ending')
          AND state.indrelid = 'purser.operator_grant_notices'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_operator_grant_notices_unsent_ending is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('purser.idx_operator_grant_notices_tenant')
          AND state.indrelid = 'purser.operator_grant_notices'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_operator_grant_notices_tenant is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
