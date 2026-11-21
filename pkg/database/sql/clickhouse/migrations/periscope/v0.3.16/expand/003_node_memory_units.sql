ALTER TABLE periscope.node_metrics_samples
    ADD COLUMN IF NOT EXISTS memory_unit_bytes UInt16 DEFAULT 1024 AFTER ram_current;
