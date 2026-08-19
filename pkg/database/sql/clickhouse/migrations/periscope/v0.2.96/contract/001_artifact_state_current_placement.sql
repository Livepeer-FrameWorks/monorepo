-- artifact_state_current: retire the old placement columns after the local-copy cutover.
--
-- is_hot is superseded by has_local_copy (added in expand); is_frozen was a placement-derived alias
-- ("no warm edge copy remains") that is fully recovered from has_local_copy + is_synced, so it is
-- removed rather than kept as a second overloaded flag. Runs in contract once every ingest binary
-- writes has_local_copy and no longer references either column. IF EXISTS keeps it idempotent against
-- a freshly-baselined cluster that never had them.
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql — the baseline carries neither
-- column, so a fresh init and an upgrade converge.
ALTER TABLE artifact_state_current DROP COLUMN IF EXISTS is_hot;
ALTER TABLE artifact_state_current DROP COLUMN IF EXISTS is_frozen;
