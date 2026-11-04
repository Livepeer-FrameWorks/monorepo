DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_media_object_authority_publishing_credential_lookup')
          AND state.indrelid = 'foghorn.media_object_authority_projection'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_media_object_authority_publishing_credential_lookup is absent, invalid, or not ready';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_sessions_execution_identity')
          AND state.indrelid = 'foghorn.ingest_sessions'::regclass
          AND state.indpred IS NULL
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_sessions_execution_identity is absent, invalid, or not ready';
    END IF;
END;
$$;
