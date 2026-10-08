DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('skipper.skipper_usage_publish_pending_idx_range')
          AND state.indrelid = 'skipper.skipper_usage'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'skipper_usage_publish_pending_idx_range is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
