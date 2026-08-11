-- One-row durable cutoff for the ONE-TIME attribution of legacy NULL-owner rows across every backend-owned table:
-- artifacts, thumbnail_task_assignment, stream_cleanup_obligation, staging_cleanup_queue, and freeze_publication_ledger.
-- A boot that finds no marker claims it (INSERT ... ON CONFLICT DO NOTHING) and, in the same transaction, stamps the
-- proven immutable cell fingerprint onto those rows' NULL backend_id. Artifact locality is recorded evidence — the
-- effective cluster (COALESCE over NULL/empty of storage then origin) is empty or this cell's cluster id (matching
-- ReconcileBillingAttribution), plus in-flight VOD multiparts and in-flight freeze attempts. The claim makes it run
-- exactly once, so a NULL backend appearing later is a genuine ownership regression cleanup fails closed on.
CREATE TABLE IF NOT EXISTS foghorn.backend_adoption (
    id         BOOLEAN PRIMARY KEY DEFAULT true,
    adopted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT backend_adoption_singleton CHECK (id)
);
