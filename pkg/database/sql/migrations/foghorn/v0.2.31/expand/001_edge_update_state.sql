CREATE TABLE IF NOT EXISTS foghorn.node_components (
    node_id VARCHAR(100) NOT NULL,
    component VARCHAR(64) NOT NULL,
    current_version TEXT,
    last_reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, component)
);

CREATE INDEX IF NOT EXISTS idx_foghorn_node_components_component ON foghorn.node_components(component);
CREATE INDEX IF NOT EXISTS idx_foghorn_node_components_reported ON foghorn.node_components(last_reported_at);

CREATE TABLE IF NOT EXISTS foghorn.node_update_state (
    node_id VARCHAR(100) PRIMARY KEY,
    target_release TEXT,
    phase VARCHAR(32) NOT NULL DEFAULT 'idle',
    started_at TIMESTAMPTZ,
    deadline TIMESTAMPTZ,
    expected_components JSONB NOT NULL DEFAULT '{}',
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE foghorn.node_update_state
    ADD COLUMN IF NOT EXISTS target_release TEXT,
    ADD COLUMN IF NOT EXISTS phase VARCHAR(32) NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deadline TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS expected_components JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_foghorn_node_update_state_phase ON foghorn.node_update_state(phase);
