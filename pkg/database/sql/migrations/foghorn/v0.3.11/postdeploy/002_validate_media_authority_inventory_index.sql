DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        JOIN pg_class AS relation ON relation.oid = state.indexrelid
        WHERE relation.oid = to_regclass('foghorn.idx_foghorn_media_authorities_inventory')
          AND state.indrelid = 'foghorn.media_authorities'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 2 AND state.indnatts = 4
          AND state.indexprs IS NULL AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['authority_kind', 'authority_id', 'authority_version', 'valid_until']::NAME[]
          AND (SELECT count(*) FROM unnest(state.indcollation::oid[]) AS key(collation_oid)
                 JOIN pg_collation AS coll ON coll.oid = key.collation_oid
                WHERE coll.collname = 'C') = 2
          AND pg_get_indexdef(relation.oid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'media authority inventory index is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
