DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_publisher_abandonments_sessions_unsettled')
          AND state.indrelid = 'foghorn.ingest_admission_abandonments'::regclass
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_publisher_abandonments_sessions_unsettled is absent, invalid, or not ready';
    END IF;
END;
$$;
