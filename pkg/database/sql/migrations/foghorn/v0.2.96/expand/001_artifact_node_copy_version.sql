-- Node-copy telemetry ordering (docs/architecture/analytics-pipeline.md).
--
-- artifact_node_copy_version_seq is the monotonic revision assigned inside the emitting
-- transaction and used as the ClickHouse ReplacingMergeTree version so concurrent
-- updates converge deterministically (wall-clock ms would tie).
--
-- artifact_nodes.last_emitted_version is a CAPTURE marker: the version of the last node-copy
-- event emitted for the row (present event records the live version, LOST records 0). The
-- reconcile pass re-emits =0 present rows so every present copy gets its GAINED once — this is
-- emission CORRECTNESS (healing rows from non-emitting writers: DVR-start / reconciler / segment
-- inserts), NOT loss recovery. The durable, authoritative record of node copies is
-- foghorn.artifact_nodes itself, re-asserted by every node's ~10s report and read directly by
-- the media plane for routing/serving. The ClickHouse artifact_node_copy_current table is a
-- derived ANALYTICS projection (one library panel); nothing operational reads it, so its loss
-- is not a scenario to design around and there is deliberately no reseed mechanism.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — the same DDL is in the
-- baseline so a fresh init and an upgrade converge on the same schema.

CREATE SEQUENCE IF NOT EXISTS foghorn.artifact_node_copy_version_seq AS BIGINT;

ALTER TABLE foghorn.artifact_nodes
    ADD COLUMN IF NOT EXISTS last_emitted_version BIGINT NOT NULL DEFAULT 0;
