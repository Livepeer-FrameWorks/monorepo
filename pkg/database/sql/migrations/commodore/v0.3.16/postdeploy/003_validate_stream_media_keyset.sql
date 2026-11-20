DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('commodore.idx_commodore_dvr_stream_history') AND indisvalid AND indisready) THEN
  RAISE EXCEPTION 'commodore index idx_commodore_dvr_stream_history is missing or not ready';
 END IF;
END $$;

DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('commodore.idx_commodore_clips_stream_history') AND indisvalid AND indisready) THEN
  RAISE EXCEPTION 'commodore index idx_commodore_clips_stream_history is missing or not ready';
 END IF;
END $$;
