-- Durable, revisioned catalog deletion markers. A catalog deletion removes the business row
-- (clips / dvr_recordings / vod_assets) and writes a compact marker here carrying Foghorn's
-- AUTHORITATIVE deletion_revision (from foghorn.artifact_catalog_revision_seq via the reconciler's
-- delete projection — the single revision writer). Ordinary readers query only the business tables
-- (absent row = not live); MintChapterPlaybackID and the snapshot revive path consult the marker to
-- block resurrecting a deleted deterministic-hash asset. Markers are permanent. kind is the artifact
-- class ('clip' | 'dvr' | 'vod'); DVR chapters are vod-kind keyed by their vod_hash.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same table in the baseline so a
-- fresh init and an upgrade converge.

-- origin_cluster_id is NOT NULL: the revive path rejects a snapshot whose source cluster differs
-- from the marker's origin (revisions are cluster-local and incomparable), so a foreign cluster can
-- never clear another origin's tombstone. Snapshots always carry a required source_cluster_id.
CREATE TABLE IF NOT EXISTS commodore.artifact_catalog_tombstones (
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    origin_cluster_id VARCHAR(100) NOT NULL,
    deletion_revision BIGINT NOT NULL,
    deleted_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, kind, artifact_hash)
);
