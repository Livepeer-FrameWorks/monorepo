-- Control-replica liveness ledger. Every Foghorn replica in a cell heartbeats its
-- own placement capability here; a cell attests schema-2 placement enforcement to
-- Commodore only when every live replica reports it.
CREATE TABLE IF NOT EXISTS foghorn.control_replicas (
    replica_id VARCHAR(255) PRIMARY KEY CHECK (btrim(replica_id) <> ''),
    release_version TEXT NOT NULL DEFAULT '',
    placement_schema_version INTEGER NOT NULL CHECK (placement_schema_version > 0),
    placement_enforced BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_control_replicas_seen
    ON foghorn.control_replicas(last_seen_at);
