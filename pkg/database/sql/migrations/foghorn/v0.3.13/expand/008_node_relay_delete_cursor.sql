-- How far each node has been sent the deletes of relay-cacheable artifacts, so
-- a node that reconnects after the fan-out window still receives them.
CREATE TABLE IF NOT EXISTS foghorn.node_relay_delete_cursor (
    node_id VARCHAR(100) PRIMARY KEY CHECK (btrim(node_id) <> ''),
    covered_through TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
