DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_session_owner') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_session_owner is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_predecessor') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_predecessor is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_expires_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_expires_range is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_commodore_auth_authz_codes_cleanup_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_commodore_auth_authz_codes_cleanup_range is missing or invalid'; END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_commodore_auth_device_codes_cleanup_range') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_commodore_auth_device_codes_cleanup_range is missing or invalid'; END IF;
END $$;
ALTER TABLE commodore.refresh_tokens VALIDATE CONSTRAINT refresh_tokens_auth_session_owner_fk;
