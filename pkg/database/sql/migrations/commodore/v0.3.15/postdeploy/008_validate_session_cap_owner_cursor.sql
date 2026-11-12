DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_users_session_cap_cursor') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_users_session_cap_cursor is missing or invalid'; END IF;
END $$;
