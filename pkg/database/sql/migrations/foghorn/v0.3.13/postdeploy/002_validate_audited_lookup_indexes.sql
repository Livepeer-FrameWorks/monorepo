DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_dvr_chapters_playback_artifact')
          AND state.indrelid = 'foghorn.dvr_chapters'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['playback_artifact_hash']::NAME[]
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_dvr_chapters_playback_artifact is absent or invalid';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_sessions_projected_node')
          AND state.indrelid = 'foghorn.ingest_sessions'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['node_id', 'started_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_sessions_projected_node is absent or invalid';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_sessions_open_trigger')
          AND state.indrelid = 'foghorn.ingest_sessions'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['node_id', 'start_trigger_uuid']::NAME[]
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_sessions_open_trigger is absent or invalid';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_sessions_stream_lookup')
          AND state.indrelid = 'foghorn.ingest_sessions'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['stream_internal_name']::NAME[]
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_sessions_stream_lookup is absent or invalid';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifact_nodes_orphan_expiry')
          AND state.indrelid = 'foghorn.artifact_nodes'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_seen_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifact_nodes_orphan_expiry is absent or invalid';
    END IF;
END;
$$;
