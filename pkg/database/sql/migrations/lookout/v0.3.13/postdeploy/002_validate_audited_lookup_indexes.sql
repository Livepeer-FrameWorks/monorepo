DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('lookout.idx_lookout_incidents_open_cluster')
          AND state.indrelid = 'lookout.incidents'::regclass
          AND state.indnkeyatts = 3 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['cluster_id', 'created_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_lookout_incidents_open_cluster is absent or invalid';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('lookout.idx_lookout_notification_outbox_incident_pending')
          AND state.indrelid = 'lookout.notification_outbox'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['incident_id', 'tenant_id']::NAME[]
          AND state.indisvalid AND state.indisready
    ) THEN
        RAISE EXCEPTION 'idx_lookout_notification_outbox_incident_pending is absent or invalid';
    END IF;
END;
$$;
