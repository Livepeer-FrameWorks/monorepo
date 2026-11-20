CREATE TABLE IF NOT EXISTS quartermaster.infrastructure_node_capacity (
    node_id VARCHAR(100) PRIMARY KEY REFERENCES quartermaster.infrastructure_nodes(node_id) ON DELETE CASCADE,
    egress_budget_bytes_per_sec BIGINT CHECK (egress_budget_bytes_per_sec > 0),
    ingress_budget_bytes_per_sec BIGINT CHECK (ingress_budget_bytes_per_sec > 0),
    revision BIGINT NOT NULL CHECK (revision > 0),
    updated_by TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
