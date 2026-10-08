DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('periscope.idx_periscope_delegated_jwt_replays_expires_at_range')
          AND state.indrelid = 'periscope.delegated_jwt_replays'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_periscope_delegated_jwt_replays_expires_at_range is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
