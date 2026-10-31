DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        WHERE state.indexrelid = to_regclass('commodore.idx_tenant_account_states_closure_failures')
          AND state.indrelid = 'commodore.tenant_account_states'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 2 AND state.indnatts = 2
          AND state.indexprs IS NULL
          AND pg_get_expr(state.indpred, state.indrelid)
              = '((quartermaster_closed_at IS NULL) AND (closure_error IS NOT NULL))'
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute
                   ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['closure_failures', 'tenant_id']::NAME[]
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'account closure failure index is absent, invalid, or has the wrong definition';
    END IF;
END;
$$;
