ALTER TABLE quartermaster.node_fingerprints
    VALIDATE CONSTRAINT chk_qm_node_identity_public_key;

DO $$
DECLARE
    invalid_indexes NAME[];
BEGIN
    SELECT array_agg(expected.index_name ORDER BY expected.index_name)
      INTO invalid_indexes
      FROM (VALUES
          ('uq_qm_fingerprints_machine'::NAME, 'fingerprint_machine_sha256'::NAME),
          ('uq_qm_fingerprints_macs'::NAME, 'fingerprint_macs_sha256'::NAME)
      ) AS expected(index_name, column_name)
      LEFT JOIN pg_namespace index_namespace
        ON index_namespace.nspname = 'quartermaster'
      LEFT JOIN pg_class index_relation
        ON index_relation.relnamespace = index_namespace.oid
       AND index_relation.relname = expected.index_name
      LEFT JOIN pg_index index_state
        ON index_state.indexrelid = index_relation.oid
     WHERE index_relation.oid IS NULL
        OR index_relation.relkind NOT IN ('i', 'I')
        OR NOT COALESCE(index_state.indisvalid, FALSE)
        OR NOT COALESCE(index_state.indisready, FALSE)
        OR NOT COALESCE(index_state.indisunique, FALSE)
        OR index_state.indrelid IS DISTINCT FROM 'quartermaster.node_fingerprints'::regclass
        OR index_state.indnkeyatts IS DISTINCT FROM 1
        OR index_state.indexprs IS NOT NULL
        OR NOT EXISTS (
            SELECT 1
            FROM pg_attribute indexed_column
            WHERE indexed_column.attrelid = index_state.indrelid
              AND indexed_column.attnum = index_state.indkey[0]
              AND indexed_column.attname = expected.column_name
        )
        OR pg_get_expr(index_state.indpred, index_state.indrelid) IS DISTINCT FROM
           format('((%I IS NOT NULL) AND (btrim(%I) <> ''''::text))', expected.column_name, expected.column_name);

    IF invalid_indexes IS NOT NULL THEN
        RAISE EXCEPTION 'node identity indexes are absent or invalid: %',
            array_to_string(invalid_indexes, ', ');
    END IF;
END;
$$;
