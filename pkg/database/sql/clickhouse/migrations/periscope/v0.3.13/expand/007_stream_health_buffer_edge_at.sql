-- Rebuffer starts and ends are transitions between the STREAM_BUFFER edges
-- Mist reports for a stream on one node. stream_health_samples.timestamp has
-- one-second resolution, which does not order a DRY and RECOVER of the same
-- second, and the periodic samples Helmsman sends between edges carry a FULL
-- state Mist never reported. buffer_edge_at holds Mist's millisecond event
-- time of an edge and is NULL for a periodic sample. Rows written before the
-- column read as edges at their timestamp.
ALTER TABLE periscope.stream_health_samples
    ADD COLUMN IF NOT EXISTS buffer_edge_at Nullable(DateTime64(3)) DEFAULT timestamp AFTER ingest_id;
