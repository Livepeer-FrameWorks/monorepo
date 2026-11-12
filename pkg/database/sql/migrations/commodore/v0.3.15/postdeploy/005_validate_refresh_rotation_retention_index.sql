DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_retention_assigned') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_retention_assigned is missing or invalid'; END IF;
END $$;
