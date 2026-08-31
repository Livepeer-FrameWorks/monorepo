CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_federated_purge_eligibility
    ON foghorn.artifacts(federated_purge_eligible_at, artifact_hash)
    WHERE federated_pointer = true
      AND status IN ('ready', 'deleted');
