-- Durable evidence that a PUSH_INPUT_CLOSE was observed for a connector for which NO ingest session
-- existed yet. Mist triggers are dispatched concurrently and can be WAL-redelivered, so a close can
-- be processed BEFORE its own PUSH_REWRITE. Without this the close acks as a no-op, the later rewrite
-- mints a fresh session and projects the source active — for a publisher that is already gone —
-- blocking a cross-node republish (the (tenant, stream) partial unique) and holding placement.
--
-- FinalizeIngestSessionClose writes a tombstone (under the same (tenant, stream) advisory lock
-- CreateIngestSession takes) when its end finds no active row; CreateIngestSession consults it in the
-- mint path and returns AlreadyEnded when a close at or after the incoming session's start is recorded
-- for the same connector — so the late rewrite is denied instead of resurrecting a dead publisher.
-- The event-time comparison (close_unix_millis >= started_at_unix_millis) keeps a genuine later
-- reconnect on a reused (node, pid) from being blocked by an older close. Rows are swept on a TTL by
-- the ingest session reaper; they only need to outlive the trigger-redelivery window.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same table in the baseline.
CREATE TABLE IF NOT EXISTS foghorn.ingest_close_tombstones (
    tenant_id            UUID NOT NULL,
    node_id              VARCHAR(100) NOT NULL,
    connector_pid        BIGINT NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    close_unix_millis    BIGINT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_close_tombstones_lookup
    ON foghorn.ingest_close_tombstones(tenant_id, node_id, connector_pid, stream_internal_name, close_unix_millis);
CREATE INDEX IF NOT EXISTS idx_foghorn_ingest_close_tombstones_created
    ON foghorn.ingest_close_tombstones(created_at);
