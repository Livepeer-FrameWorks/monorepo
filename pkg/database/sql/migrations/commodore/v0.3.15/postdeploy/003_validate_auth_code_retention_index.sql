DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_commodore_auth_authz_codes_consumed_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_commodore_auth_authz_codes_consumed_range is missing or invalid'; END IF;
END $$;
