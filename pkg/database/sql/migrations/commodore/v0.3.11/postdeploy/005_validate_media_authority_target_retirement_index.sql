DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        JOIN pg_class AS relation ON relation.oid = state.indexrelid
        WHERE relation.oid = to_regclass('commodore.idx_media_authority_targets_retired')
          AND state.indrelid = 'commodore.media_authority_targets'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 4 AND state.indnatts = 4
          AND state.indexprs IS NULL
          AND pg_get_expr(state.indpred, state.indrelid) = '(correction_until IS NOT NULL)'
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['correction_until', 'authority_kind', 'authority_id', 'cell_id']::NAME[]
          AND pg_get_indexdef(relation.oid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'media authority target retirement index is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
