-- Whole-node artifact report ordering. Foghorn issues each node control connection a monotonic
-- ownership fence from node_control_fence_seq when it registers; a report is ordered by
-- (connection_fence, report_seq), where report_seq is the sidecar's per-connection counter. Each
-- poller report persists on its own goroutine, so DB commit order is not report order; the
-- upsert/orphan paths advance the watermark via an atomic compare-and-set and drop any report that
-- loses. A reconnect gets a strictly higher fence and supersedes; a delayed report from a
-- superseded connection loses. Restart-safe without wall-clock ordering, and a nextval() per
-- connection (not a report hot path) that stays monotonic across a Redis restart.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same objects in the baseline so a
-- fresh init and an upgrade converge.
CREATE SEQUENCE IF NOT EXISTS foghorn.node_control_fence_seq AS BIGINT;

CREATE TABLE IF NOT EXISTS foghorn.node_artifact_report_watermark (
    node_id VARCHAR(100) PRIMARY KEY,
    connection_fence BIGINT NOT NULL,
    seq BIGINT NOT NULL
);
