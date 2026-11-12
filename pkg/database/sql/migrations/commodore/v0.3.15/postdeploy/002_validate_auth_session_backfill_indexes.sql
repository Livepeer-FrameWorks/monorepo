DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_pending_session_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_pending_session_range is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_pending_session_owner_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_pending_session_owner_range is missing or invalid'; END IF;
END $$;
