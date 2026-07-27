-- Backoff gate for the artifact-event outbox drain. Before this, a failed row reset
-- claimed_at and was immediately re-eligible; combined with the oldest-first LIMIT-batch
-- claim, a cluster of permanently-failing (poison) rows would head-of-line starve every
-- newer lifecycle / node-copy event indefinitely. next_retry_at holds a row out of the
-- eligible set until its backoff elapses, so newer rows are always reachable.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.

ALTER TABLE foghorn.artifact_event_outbox
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ;
