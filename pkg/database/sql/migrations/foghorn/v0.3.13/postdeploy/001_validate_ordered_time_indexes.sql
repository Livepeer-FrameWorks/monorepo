DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_created_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_created_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_frozen_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['frozen_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_frozen_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_dvr_backfill_pending_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['ended_at', 'artifact_hash']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_dvr_backfill_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_federated_purge_eligibility_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['federated_purge_eligible_at', 'artifact_hash']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_federated_purge_eligibility_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifact_nodes_seen_range')
          AND state.indrelid = 'foghorn.artifact_nodes'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_seen_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifact_nodes_seen_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifact_nodes_cached_range')
          AND state.indrelid = 'foghorn.artifact_nodes'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['cached_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifact_nodes_cached_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_sessions_pending_projection_range')
          AND state.indrelid = 'foghorn.ingest_sessions'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['started_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_sessions_pending_projection_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_close_tombstones_created_range')
          AND state.indrelid = 'foghorn.ingest_close_tombstones'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_close_tombstones_created_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_admission_abandonments_created_range')
          AND state.indrelid = 'foghorn.ingest_admission_abandonments'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_admission_abandonments_created_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_offline_effects_pending_range')
          AND state.indrelid = 'foghorn.ingest_offline_effects'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_offline_effects_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_offline_effects_terminal_range')
          AND state.indrelid = 'foghorn.ingest_offline_effects'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['updated_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_offline_effects_terminal_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_admission_effects_pending_range')
          AND state.indrelid = 'foghorn.ingest_admission_effects'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_admission_effects_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_ingest_admission_effects_terminal_range')
          AND state.indrelid = 'foghorn.ingest_admission_effects'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['updated_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_ingest_admission_effects_terminal_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_admission_push_target_revisions_created_range')
          AND state.indrelid = 'foghorn.admission_push_target_revisions'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_admission_push_target_revisions_created_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_node_admission_proof_nonces_expiry_range')
          AND state.indrelid = 'foghorn.node_admission_proof_nonces'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['expires_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_node_admission_proof_nonces_expiry_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_push_target_status_outbox_due_range')
          AND state.indrelid = 'foghorn.push_target_status_outbox'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_push_target_status_outbox_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_managed_stream_active_cluster_outbox_due_range')
          AND state.indrelid = 'foghorn.managed_stream_active_cluster_outbox'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_managed_stream_active_cluster_outbox_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_config_seed_apply_ack_outbox_due_range')
          AND state.indrelid = 'foghorn.config_seed_apply_ack_outbox'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_config_seed_apply_ack_outbox_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_signing_key_use_outbox_due_range')
          AND state.indrelid = 'foghorn.signing_key_use_outbox'::regclass
          AND state.indnkeyatts = 2 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at', 'id']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_signing_key_use_outbox_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_node_components_reported_range')
          AND state.indrelid = 'foghorn.node_components'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_reported_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_node_components_reported_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_staging_cleanup_due_range')
          AND state.indrelid = 'foghorn.staging_cleanup_queue'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_staging_cleanup_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_freeze_pub_ledger_age_range')
          AND state.indrelid = 'foghorn.freeze_publication_ledger'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_freeze_pub_ledger_age_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_thumb_recovery_due_range')
          AND state.indrelid = 'foghorn.thumbnail_task_assignment'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['recovery_next_attempt_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_thumb_recovery_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_thumb_unprojected_range')
          AND state.indrelid = 'foghorn.thumbnail_task_assignment'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['updated_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_thumb_unprojected_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_thumb_reassert_due_range')
          AND state.indrelid = 'foghorn.thumbnail_task_assignment'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['deterministic_reassert_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_thumb_reassert_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_stream_cleanup_due_range')
          AND state.indrelid = 'foghorn.stream_cleanup_obligation'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['next_attempt_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_stream_cleanup_due_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifact_event_outbox_pending_range')
          AND state.indrelid = 'foghorn.artifact_event_outbox'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['created_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifact_event_outbox_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_domain_event_outbox_pending_range')
          AND state.indrelid = 'foghorn.domain_event_outbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_domain_event_outbox_pending_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_domain_event_outbox_completed_range')
          AND state.indrelid = 'foghorn.domain_event_outbox'::regclass
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_domain_event_outbox_completed_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_creation_commands_accepted_range')
          AND state.indrelid = 'foghorn.artifact_creation_commands'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['updated_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_creation_commands_accepted_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_creation_commands_terminal_gc_range')
          AND state.indrelid = 'foghorn.artifact_creation_commands'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['consumed_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_creation_commands_terminal_gc_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_media_authority_apply_audit_retention_range')
          AND state.indrelid = 'foghorn.media_authority_apply_audit'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['observed_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_media_authority_apply_audit_retention_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_control_replicas_seen_range')
          AND state.indrelid = 'foghorn.control_replicas'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_seen_at']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_control_replicas_seen_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_federated_purge_recovery_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['federated_purge_lease_until']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_federated_purge_recovery_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_artifacts_retention_range')
          AND state.indrelid = 'foghorn.artifacts'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NOT NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['retention_until']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_artifacts_retention_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_node_outputs_updated_range')
          AND state.indrelid = 'foghorn.node_outputs'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_updated']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_node_outputs_updated_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_node_lifecycle_updated_range')
          AND state.indrelid = 'foghorn.node_lifecycle'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['last_updated']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_node_lifecycle_updated_range is absent, invalid, or hash-sharded';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_index state
        WHERE state.indexrelid = to_regclass('foghorn.idx_foghorn_media_authorities_expiry_range')
          AND state.indrelid = 'foghorn.media_authorities'::regclass
          AND state.indnkeyatts = 1 AND state.indexprs IS NULL
          AND state.indpred IS NULL
          AND (SELECT array_agg(attribute.attname ORDER BY key.ordinality)
                 FROM unnest(state.indkey::smallint[]) WITH ORDINALITY AS key(attnum, ordinality)
                 JOIN pg_attribute attribute ON attribute.attrelid = state.indrelid AND attribute.attnum = key.attnum
                WHERE key.ordinality <= state.indnkeyatts) = ARRAY['valid_until']::NAME[]
          AND state.indisvalid AND state.indisready
          AND pg_get_indexdef(state.indexrelid) !~ ' HASH([, )])'
    ) THEN
        RAISE EXCEPTION 'idx_foghorn_media_authorities_expiry_range is absent, invalid, or hash-sharded';
    END IF;
END;
$$;
