CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending_v03
    ON foghorn.ingest_admission_effects(next_attempt_at, id)
    WHERE state IN ('pending', 'pending_v2');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_effects_pending_fence_v03
    ON foghorn.ingest_admission_effects(tenant_id, stream_internal_name, source_revision)
    WHERE state IN ('pending', 'pending_v2');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_ingest_admission_effects_terminal_v03
    ON foghorn.ingest_admission_effects(updated_at)
    WHERE state IN ('applied', 'superseded', 'applied_v2', 'superseded_v2');
