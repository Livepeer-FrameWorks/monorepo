-- Per-cell placement capability as attested by that cell's Foghorn in media
-- authority acknowledgements. The compiler issues the first schema-2 tenant
-- authority only when every target cell has attested enforcement readiness.
CREATE TABLE IF NOT EXISTS commodore.media_cell_placement_capabilities (
    cell_id VARCHAR(255) PRIMARY KEY CHECK (btrim(cell_id) <> ''),
    max_schema_version INTEGER NOT NULL CHECK (max_schema_version > 0),
    enforcement_ready BOOLEAN NOT NULL DEFAULT FALSE,
    live_replicas INTEGER NOT NULL DEFAULT 0 CHECK (live_replicas >= 0),
    first_ready_at TIMESTAMPTZ,
    attested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
