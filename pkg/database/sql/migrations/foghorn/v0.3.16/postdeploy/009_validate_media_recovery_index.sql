DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('foghorn.idx_foghorn_artifacts_media_recovery') AND indisvalid AND indisready) THEN
  RAISE EXCEPTION 'foghorn index idx_foghorn_artifacts_media_recovery is missing or not ready';
 END IF;
END $$;
