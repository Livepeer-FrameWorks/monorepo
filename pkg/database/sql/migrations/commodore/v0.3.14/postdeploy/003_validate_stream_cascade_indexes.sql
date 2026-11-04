DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        WHERE state.indexrelid = to_regclass('commodore.idx_commodore_push_targets_stream_fk')
          AND state.indrelid = 'commodore.push_targets'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 1 AND state.indnatts = 1
          AND state.indexprs IS NULL AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute
                   ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['stream_id']::NAME[]
    ) THEN
        RAISE EXCEPTION 'push target stream cascade index is absent, invalid, or has the wrong definition';
    END IF;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        WHERE state.indexrelid = to_regclass('commodore.idx_commodore_policy_bundle_versions_stream_fk')
          AND state.indrelid = 'commodore.policy_bundle_versions'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 1 AND state.indnatts = 1
          AND state.indexprs IS NULL AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute
                   ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['stream_id']::NAME[]
    ) THEN
        RAISE EXCEPTION 'policy bundle stream cascade index is absent, invalid, or has the wrong definition';
    END IF;
END;
$$;
