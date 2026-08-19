-- Bind the accepted virtual media cluster to the session that was accepted into it.
--
-- The cluster accepted at admission is a property of the publisher session, not of the node's
-- current registration. Claim renewal and release replay this value for the session's whole life.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql — same column in the baseline.
ALTER TABLE foghorn.ingest_sessions
    ADD COLUMN IF NOT EXISTS ingest_cluster_id VARCHAR(100);
