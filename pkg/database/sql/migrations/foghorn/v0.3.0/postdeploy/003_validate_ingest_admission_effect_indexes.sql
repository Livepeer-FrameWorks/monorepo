DO $$
DECLARE
    invalid_indexes NAME[];
BEGIN
    SELECT array_agg(expected.index_name ORDER BY expected.index_name)
      INTO invalid_indexes
      FROM (VALUES
          ('idx_foghorn_ingest_admission_effects_pending_v03'::NAME,
              ARRAY['next_attempt_at', 'id']::NAME[]),
          ('idx_foghorn_ingest_admission_effects_pending_fence_v03'::NAME,
              ARRAY['tenant_id', 'stream_internal_name', 'source_revision']::NAME[]),
          ('idx_foghorn_ingest_admission_effects_terminal_v03'::NAME,
              ARRAY['updated_at']::NAME[])
      ) AS expected(index_name, key_columns)
      LEFT JOIN pg_namespace index_namespace
        ON index_namespace.nspname = 'foghorn'
      LEFT JOIN pg_class index_relation
        ON index_relation.relnamespace = index_namespace.oid
       AND index_relation.relname = expected.index_name
      LEFT JOIN pg_index index_state
        ON index_state.indexrelid = index_relation.oid
     WHERE index_relation.oid IS NULL
        OR index_relation.relkind NOT IN ('i', 'I')
        OR NOT COALESCE(index_state.indisvalid, FALSE)
        OR NOT COALESCE(index_state.indisready, FALSE)
        OR index_state.indrelid IS DISTINCT FROM 'foghorn.ingest_admission_effects'::regclass
        OR index_state.indnkeyatts IS DISTINCT FROM cardinality(expected.key_columns)
        OR index_state.indexprs IS NOT NULL
        OR index_state.indpred IS NULL
        OR (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
              FROM unnest(index_state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
              JOIN pg_attribute attribute
                ON attribute.attrelid = index_state.indrelid
               AND attribute.attnum = key.attnum
             WHERE key.ordinality <= index_state.indnkeyatts) IS DISTINCT FROM expected.key_columns;

    IF invalid_indexes IS NOT NULL THEN
        RAISE EXCEPTION 'ingest admission effect indexes are absent or invalid: %',
            array_to_string(invalid_indexes, ', ');
    END IF;
END;
$$;
