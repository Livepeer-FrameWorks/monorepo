DO $$
BEGIN
  IF EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'commodore'
       AND table_name = 'clips'
       AND column_name = 'size_bytes'
       AND data_type <> 'bigint'
  ) THEN
    ALTER TABLE commodore.clips
      ALTER COLUMN size_bytes TYPE BIGINT USING size_bytes::bigint;
  END IF;

  IF EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'commodore'
       AND table_name = 'dvr_recordings'
       AND column_name = 'size_bytes'
       AND data_type <> 'bigint'
  ) THEN
    ALTER TABLE commodore.dvr_recordings
      ALTER COLUMN size_bytes TYPE BIGINT USING size_bytes::bigint;
  END IF;
END $$;
