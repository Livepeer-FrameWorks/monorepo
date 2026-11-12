DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_auth_sessions_retention_idle') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_auth_sessions_retention_idle is missing or invalid'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_auth_sessions_retention_revoked') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_auth_sessions_retention_revoked is missing or invalid'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_retention_session') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_retention_session is missing or invalid'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_retention_unassigned') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_retention_unassigned is missing or invalid'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('commodore.idx_refresh_tokens_live_legacy_owner') AND indisvalid AND indisready
    ) THEN RAISE EXCEPTION 'commodore.idx_refresh_tokens_live_legacy_owner is missing or invalid'; END IF;
END $$;
