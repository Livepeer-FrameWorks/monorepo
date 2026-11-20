ALTER TABLE periscope.node_state_current
    ADD COLUMN IF NOT EXISTS egress_budget UInt64 DEFAULT 0 AFTER bw_limit,
    ADD COLUMN IF NOT EXISTS budget_source LowCardinality(String) DEFAULT '' AFTER egress_budget;
