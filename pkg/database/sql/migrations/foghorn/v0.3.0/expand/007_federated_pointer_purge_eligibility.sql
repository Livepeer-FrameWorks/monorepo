ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS federated_purge_eligible_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

COMMENT ON COLUMN foghorn.artifacts.federated_purge_eligible_at IS
    'Stable age anchor for federated-pointer retirement; ordinary metadata and inventory writers must not update it.';
