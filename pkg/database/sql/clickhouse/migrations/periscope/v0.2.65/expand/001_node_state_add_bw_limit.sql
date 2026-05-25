ALTER TABLE periscope.node_state_current ADD COLUMN IF NOT EXISTS bw_limit UInt64 DEFAULT 0;
