DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index AS state
        JOIN pg_class AS relation ON relation.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
        WHERE namespace.nspname = 'commodore'
          AND relation.oid = to_regclass('commodore.idx_media_authority_distribution_cell_ack')
          AND state.indrelid = 'commodore.media_authority_distribution'::regclass
          AND state.indisvalid AND state.indisready
          AND state.indnkeyatts = 3 AND state.indnatts = 5
          AND state.indexprs IS NULL AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum)
              = ARRAY['cell_id', 'authority_kind', 'authority_id', 'highest_acknowledged_version', 'last_acknowledged_at']::NAME[]
          AND (SELECT count(*) FROM unnest(state.indcollation::oid[]) WITH ORDINALITY AS key(collation_oid, ordinality)
                 JOIN pg_collation AS coll ON coll.oid = key.collation_oid
                WHERE key.ordinality IN (2, 3) AND coll.collname = 'C') = 2
          AND pg_get_indexdef(relation.oid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'media authority cell acknowledgement index is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
