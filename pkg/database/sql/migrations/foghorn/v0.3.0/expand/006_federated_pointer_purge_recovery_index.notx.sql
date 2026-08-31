CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foghorn_artifacts_federated_purge_recovery
    ON foghorn.artifacts(federated_purge_lease_until)
    WHERE federated_pointer = true
      AND status = 'deleted'
      AND federated_purge_token IS NOT NULL;
