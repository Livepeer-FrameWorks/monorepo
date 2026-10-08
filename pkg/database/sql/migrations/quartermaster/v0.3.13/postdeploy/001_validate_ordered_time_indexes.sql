DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('quartermaster.idx_quartermaster_delegated_jwt_replays_expires_at_range')
          AND state.indrelid = 'quartermaster.delegated_jwt_replays'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_quartermaster_delegated_jwt_replays_expires_at_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('quartermaster.idx_qm_service_event_outbox_pending_range')
          AND state.indrelid = 'quartermaster.service_event_outbox'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_qm_service_event_outbox_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('quartermaster.idx_quartermaster_domain_event_outbox_pending_range')
          AND state.indrelid = 'quartermaster.domain_event_outbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_quartermaster_domain_event_outbox_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('quartermaster.idx_quartermaster_domain_event_outbox_completed_range')
          AND state.indrelid = 'quartermaster.domain_event_outbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_quartermaster_domain_event_outbox_completed_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('quartermaster.idx_qm_navigator_custom_domain_outbox_pending_range')
          AND state.indrelid = 'quartermaster.navigator_custom_domain_outbox'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_qm_navigator_custom_domain_outbox_pending_range is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
