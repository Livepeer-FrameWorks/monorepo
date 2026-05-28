-- Peer-relay artifact federation — expand migration.
-- Adds role discriminator and writer-authoritative is_complete flag to
-- foghorn.artifact_nodes so the resolver can identify nodes that hold
-- the canonical full file (eligible to serve cross-cluster peer-relay
-- reads when the artifact is hot-but-unsynced on S3).
--
-- Defaults make existing rows ineligible (role='cache', is_complete=false),
-- which is the safe outcome — no backfill needed. New writes from
-- updated sidecars carry truth forward via the origin-wins upsert in
-- repos.go.
ALTER TABLE foghorn.artifact_nodes
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'cache'
        CHECK (role IN ('origin', 'cache')),
    ADD COLUMN IF NOT EXISTS is_complete BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_foghorn_artifact_nodes_origin_complete
    ON foghorn.artifact_nodes(artifact_hash)
    WHERE role = 'origin' AND is_complete = true AND is_orphaned = false;
