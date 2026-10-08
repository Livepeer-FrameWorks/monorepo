CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_created_range ON foghorn.artifacts(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_frozen_range ON foghorn.artifacts(frozen_at ASC) WHERE frozen_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_dvr_backfill_pending_range
    ON foghorn.artifacts(ended_at ASC, artifact_hash)
    WHERE artifact_type = 'dvr'
      AND status IN ('completed', 'completed_partial', 'failed', 'ready')
      AND ended_at IS NOT NULL
      AND dvr_chapter_mode IS NOT NULL
      AND dvr_chapter_mode != ''
      AND dvr_chapter_backfill_complete = false;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_federated_purge_eligibility_range
    ON foghorn.artifacts(federated_purge_eligible_at ASC, artifact_hash)
    WHERE federated_pointer = true
      AND status IN ('ready', 'deleted');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifact_nodes_seen_range ON foghorn.artifact_nodes(last_seen_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifact_nodes_cached_range ON foghorn.artifact_nodes(cached_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_sessions_pending_projection_range
    ON foghorn.ingest_sessions(started_at ASC)
    WHERE ended_at IS NULL AND projection_state = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_close_tombstones_created_range
    ON foghorn.ingest_close_tombstones(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_abandonments_created_range
    ON foghorn.ingest_admission_abandonments(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_offline_effects_pending_range
    ON foghorn.ingest_offline_effects(next_attempt_at ASC, id)
    WHERE state = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_offline_effects_terminal_range
    ON foghorn.ingest_offline_effects(updated_at ASC)
    WHERE state IN ('applied', 'superseded', 'failed');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending_range
    ON foghorn.ingest_admission_effects(next_attempt_at ASC, id)
    WHERE state IN ('pending', 'pending_v2');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_effects_terminal_range
    ON foghorn.ingest_admission_effects(updated_at ASC)
    WHERE state IN ('applied', 'superseded', 'applied_v2', 'superseded_v2');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_admission_push_target_revisions_created_range
    ON foghorn.admission_push_target_revisions(created_at ASC, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_node_admission_proof_nonces_expiry_range
    ON foghorn.node_admission_proof_nonces(expires_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_push_target_status_outbox_due_range
    ON foghorn.push_target_status_outbox(next_attempt_at ASC, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_managed_stream_active_cluster_outbox_due_range
    ON foghorn.managed_stream_active_cluster_outbox(next_attempt_at ASC, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_config_seed_apply_ack_outbox_due_range
    ON foghorn.config_seed_apply_ack_outbox(next_attempt_at ASC, id) WHERE pending;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_signing_key_use_outbox_due_range
    ON foghorn.signing_key_use_outbox(next_attempt_at ASC, id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_node_components_reported_range ON foghorn.node_components(last_reported_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_staging_cleanup_due_range ON foghorn.staging_cleanup_queue(next_attempt_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_freeze_pub_ledger_age_range ON foghorn.freeze_publication_ledger(created_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_thumb_recovery_due_range
    ON foghorn.thumbnail_task_assignment(recovery_next_attempt_at ASC)
    WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_thumb_unprojected_range
    ON foghorn.thumbnail_task_assignment(updated_at ASC)
    WHERE status = 'published' AND deterministic_projected_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_thumb_reassert_due_range
    ON foghorn.thumbnail_task_assignment(deterministic_reassert_at ASC)
    WHERE deterministic_reassert_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_stream_cleanup_due_range ON foghorn.stream_cleanup_obligation(next_attempt_at ASC) WHERE status = 'pending';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifact_event_outbox_pending_range
    ON foghorn.artifact_event_outbox(created_at ASC)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_domain_event_outbox_pending_range
    ON foghorn.domain_event_outbox (enqueued_at ASC, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_domain_event_outbox_completed_range
    ON foghorn.domain_event_outbox (completed_at ASC)
    WHERE completed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_creation_commands_accepted_range
    ON foghorn.artifact_creation_commands(updated_at ASC)
    WHERE status = 'accepted';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_creation_commands_terminal_gc_range
    ON foghorn.artifact_creation_commands(consumed_at ASC)
    WHERE status IN ('committed', 'rejected') AND consumed_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_authority_apply_audit_retention_range
    ON foghorn.media_authority_apply_audit(observed_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_control_replicas_seen_range
    ON foghorn.control_replicas(last_seen_at ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_federated_purge_recovery_range
    ON foghorn.artifacts(federated_purge_lease_until ASC)
    WHERE federated_pointer = true
      AND status = 'deleted'
      AND federated_purge_token IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_retention_range ON foghorn.artifacts(retention_until ASC) WHERE retention_until IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_node_outputs_updated_range ON foghorn.node_outputs(last_updated ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_node_lifecycle_updated_range ON foghorn.node_lifecycle(last_updated ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_media_authorities_expiry_range
    ON foghorn.media_authorities(valid_until ASC);
