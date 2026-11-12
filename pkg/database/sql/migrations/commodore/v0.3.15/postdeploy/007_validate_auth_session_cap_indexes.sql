DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_auth_sessions_owner_active') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_auth_sessions_owner_active is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_auth_sessions_owner_oldest') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_auth_sessions_owner_oldest is missing or invalid'; END IF;
END $$;
