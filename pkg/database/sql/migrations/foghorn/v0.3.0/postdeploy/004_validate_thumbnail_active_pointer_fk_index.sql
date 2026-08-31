DO $$
DECLARE
    index_state RECORD;
BEGIN
    SELECT relation.oid, state.indisvalid, state.indisready, state.indrelid,
           state.indnkeyatts, state.indexprs, state.indpred,
           attribute.attname AS key_column
      INTO index_state
      FROM pg_class relation
      JOIN pg_index state ON state.indexrelid = relation.oid
      LEFT JOIN pg_attribute attribute
        ON attribute.attrelid = state.indrelid
       AND attribute.attnum = state.indkey[0]
     WHERE relation.oid = to_regclass('foghorn.idx_foghorn_thumbnail_active_pointer_version');

    IF index_state.oid IS NULL
       OR NOT COALESCE(index_state.indisvalid, FALSE)
       OR NOT COALESCE(index_state.indisready, FALSE)
       OR index_state.indrelid IS DISTINCT FROM 'foghorn.thumbnail_active_pointer'::regclass
       OR index_state.indnkeyatts IS DISTINCT FROM 1
       OR index_state.indexprs IS NOT NULL
       OR index_state.indpred IS NOT NULL
       OR index_state.key_column IS DISTINCT FROM 'active_version' THEN
        RAISE EXCEPTION 'thumbnail active-pointer FK index is absent or invalid: %',
            'idx_foghorn_thumbnail_active_pointer_version';
    END IF;
END;
$$;
