-- Processing speed telemetry on artifact lifecycle events (clip/VOD done
-- stages): how fast the processing job's feeder actually ran and what gated
-- it (MistServer rate-controller aggregates relayed via Helmsman → Foghorn).
-- Nullable so pre-existing rows and non-processing stages stay untouched.
-- Identical columns are added to the baseline periscope.sql so a fresh init
-- and an upgrade converge on the same schema.

ALTER TABLE artifact_events
    ADD COLUMN IF NOT EXISTS processing_wall_ms Nullable(UInt64),
    ADD COLUMN IF NOT EXISTS speed_min_x Nullable(Float32),
    ADD COLUMN IF NOT EXISTS speed_avg_x Nullable(Float32),
    ADD COLUMN IF NOT EXISTS speed_max_x Nullable(Float32),
    ADD COLUMN IF NOT EXISTS hard_slow_ticks Nullable(UInt32),
    ADD COLUMN IF NOT EXISTS stale_hold_ticks Nullable(UInt32),
    ADD COLUMN IF NOT EXISTS lockout_ticks Nullable(UInt32),
    ADD COLUMN IF NOT EXISTS drain_ms Nullable(UInt64);
