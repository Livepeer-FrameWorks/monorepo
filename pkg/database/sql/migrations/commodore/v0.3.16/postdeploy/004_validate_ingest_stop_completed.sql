DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('commodore.idx_commodore_ingest_stop_completed') AND indisvalid AND indisready) THEN
  RAISE EXCEPTION 'commodore index idx_commodore_ingest_stop_completed is missing or not ready';
 END IF;
END $$;

ALTER TABLE commodore.media_authority_refresh_obligations VALIDATE CONSTRAINT chk_media_authority_obligation_lane;
