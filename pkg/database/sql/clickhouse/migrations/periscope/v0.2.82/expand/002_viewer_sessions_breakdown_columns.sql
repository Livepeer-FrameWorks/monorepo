-- Add USER_END breakdown arrays to viewer_sessions_final.
--
-- MistServer src/session.cpp emits per-stream / per-connector /
-- per-host parallel arrays for multi-stream sessions (streamSummary +
-- streamTimes, etc.); preserving them as first-class columns lets
-- downstream attribution split a single session's minutes across all
-- entries instead of dropping all of them on the first comma-joined
-- name.
--
-- Additive ALTER with defaults; safe in expand.
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql

ALTER TABLE viewer_sessions_final
    ADD COLUMN IF NOT EXISTS stream_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [];

ALTER TABLE viewer_sessions_final
    ADD COLUMN IF NOT EXISTS connector_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [];

ALTER TABLE viewer_sessions_final
    ADD COLUMN IF NOT EXISTS host_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [];
