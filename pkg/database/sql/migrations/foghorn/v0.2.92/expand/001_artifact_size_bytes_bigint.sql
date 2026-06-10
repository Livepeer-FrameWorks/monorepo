DO $$
BEGIN
  IF EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'foghorn'
       AND table_name = 'artifacts'
       AND column_name = 'size_bytes'
       AND data_type <> 'bigint'
  ) THEN
    ALTER TABLE foghorn.artifacts
      ALTER COLUMN size_bytes TYPE BIGINT USING size_bytes::bigint;
  END IF;

  IF EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'foghorn'
       AND table_name = 'artifact_nodes'
       AND column_name = 'size_bytes'
       AND data_type <> 'bigint'
  ) THEN
    ALTER TABLE foghorn.artifact_nodes
      ALTER COLUMN size_bytes TYPE BIGINT USING size_bytes::bigint;
  END IF;
END $$;
