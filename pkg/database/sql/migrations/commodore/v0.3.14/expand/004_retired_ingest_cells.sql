-- Cells an operator recorded as permanently removed. A session stop that cannot
-- reach a retired cell counts as delivered there; a retired cell that answers
-- still receives the stop.
CREATE TABLE IF NOT EXISTS commodore.retired_ingest_cells (
    cell_id TEXT PRIMARY KEY CHECK (btrim(cell_id) <> ''),
    retired_by TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    retired_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
