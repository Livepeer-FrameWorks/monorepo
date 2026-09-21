-- The coalescing index is a point-lookup arbiter for ON CONFLICT, so hash
-- sharding is acceptable here; it must be valid, unique, and partial.
DO $$
DECLARE
    index_state RECORD;
BEGIN
    SELECT relation.oid, state.indisvalid, state.indisready, state.indisunique, state.indrelid,
           state.indnkeyatts, state.indexprs, state.indpred,
           (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
              FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
              JOIN pg_attribute attribute
                ON attribute.attrelid = state.indrelid
               AND attribute.attnum = key.attnum
             WHERE key.ordinality <= state.indnkeyatts) AS key_columns
      INTO index_state
      FROM pg_class relation
      JOIN pg_index state ON state.indexrelid = relation.oid
     WHERE relation.oid = to_regclass('quartermaster.idx_quartermaster_media_authority_refresh_coalesced');

    IF index_state.oid IS NULL
       OR NOT COALESCE(index_state.indisvalid, FALSE)
       OR NOT COALESCE(index_state.indisready, FALSE)
       OR NOT COALESCE(index_state.indisunique, FALSE)
       OR index_state.indrelid IS DISTINCT FROM 'quartermaster.media_authority_refresh_outbox'::regclass
       OR index_state.indnkeyatts IS DISTINCT FROM 1
       OR index_state.indexprs IS NOT NULL
       OR index_state.indpred IS NULL
       OR index_state.key_columns IS DISTINCT FROM ARRAY['coalesce_key']::NAME[] THEN
        RAISE EXCEPTION 'Quartermaster media authority coalescing index idx_quartermaster_media_authority_refresh_coalesced is absent or invalid';
    END IF;
END;
$$;
