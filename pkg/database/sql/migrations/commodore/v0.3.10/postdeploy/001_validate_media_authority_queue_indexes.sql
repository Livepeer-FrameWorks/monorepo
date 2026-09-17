DO $$
DECLARE
    invalid_indexes NAME[];
BEGIN
    SELECT array_agg(expected.index_name ORDER BY expected.index_name)
      INTO invalid_indexes
      FROM (VALUES
          ('idx_media_authority_deliveries_due_v2'::NAME, 'commodore.media_authority_deliveries'::regclass,
              ARRAY['next_attempt_at', 'created_at']::NAME[], TRUE),
          ('idx_media_authority_versions_expiry_v2'::NAME, 'commodore.media_authority_versions'::regclass,
              ARRAY['valid_until', 'authority_kind', 'authority_id', 'authority_version']::NAME[], FALSE),
          ('idx_media_authority_refresh_inbox_due_v2'::NAME, 'commodore.media_authority_refresh_inbox'::regclass,
              ARRAY['next_attempt_at', 'created_at']::NAME[], TRUE),
          ('idx_media_authority_refresh_inbox_completed_v2'::NAME, 'commodore.media_authority_refresh_inbox'::regclass,
              ARRAY['completed_at', 'source_service', 'source_event_id']::NAME[], TRUE)
      ) AS expected(index_name, table_oid, key_columns, partial)
      LEFT JOIN pg_namespace index_namespace
        ON index_namespace.nspname = 'commodore'
      LEFT JOIN pg_class index_relation
        ON index_relation.relnamespace = index_namespace.oid
       AND index_relation.relname = expected.index_name
      LEFT JOIN pg_index index_state
        ON index_state.indexrelid = index_relation.oid
     WHERE index_relation.oid IS NULL
        OR index_relation.relkind NOT IN ('i', 'I')
        OR NOT COALESCE(index_state.indisvalid, FALSE)
        OR NOT COALESCE(index_state.indisready, FALSE)
        OR index_state.indrelid IS DISTINCT FROM expected.table_oid
        OR index_state.indnkeyatts IS DISTINCT FROM cardinality(expected.key_columns)
        OR index_state.indexprs IS NOT NULL
        OR (index_state.indpred IS NOT NULL) IS DISTINCT FROM expected.partial
        OR COALESCE(pg_get_indexdef(index_relation.oid), '') ~ ' HASH([, )])'
        OR (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
              FROM unnest(index_state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
              JOIN pg_attribute attribute
                ON attribute.attrelid = index_state.indrelid
               AND attribute.attnum = key.attnum
             WHERE key.ordinality <= index_state.indnkeyatts) IS DISTINCT FROM expected.key_columns;

    IF invalid_indexes IS NOT NULL THEN
        RAISE EXCEPTION 'media authority queue indexes are absent, invalid, or hash-sharded: %',
            array_to_string(invalid_indexes, ', ');
    END IF;
END;
$$;
