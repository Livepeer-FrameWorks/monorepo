DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('commodore.idx_commodore_users_verification_token')
          AND state.indrelid = 'commodore.users'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['verification_token']::NAME[]
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_commodore_users_verification_token is absent or invalid';
    END IF;
END;
$$;
