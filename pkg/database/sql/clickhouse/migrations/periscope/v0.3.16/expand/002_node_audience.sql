ALTER TABLE periscope.node_state_current
    ADD COLUMN IF NOT EXISTS viewers UInt32 DEFAULT 0 AFTER budget_source;
