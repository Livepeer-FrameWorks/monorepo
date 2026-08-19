-- Give an ingest placement claim an OWNER, not just a cluster.
--
-- active_ingest_cluster_id names which cluster is publishing a stream, which is enough to route to
-- but not enough to safely give back: a release fenced only on the cluster cannot tell the attempt
-- that took the claim from a different node in that same cluster, nor from an attempt that took no
-- claim at all (a Foghorn admitting from its validation cache during a Commodore outage). Both cases
-- would clear a live publisher's placement.
--
-- The token is the Mist trigger UUID of the publisher connection that took the claim (X-Trigger-UUID,
-- unique per connection for its lifetime — the same identity foghorn.ingest_sessions keys a session
-- by). Every writer that acquires or refreshes a claim stamps it; release matches on it and clears
-- nothing otherwise.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same column in the baseline.
ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS active_ingest_claim_id VARCHAR(64);
