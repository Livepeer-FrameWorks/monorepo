-- command_ack_leased_until is the ack-drain CLAIM LEASE, a separate axis from the retry
-- schedule (command_ack_next_at): the drain stamps it NOW() + a fixed lease when it claims a
-- due obligation, and the due-query EXCLUDES a row whose lease has not passed, so a claimed row
-- cannot be reclaimed by another replica while its ack RPC is in flight. The retry backoff
-- lives ONLY on command_ack_next_at, pushed only on a non-discharging outcome. A NULL lease (an
-- obligation outstanding at upgrade, before this column existed) is treated as unleased and
-- immediately claimable, so no backfill is needed.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same column in the baseline
-- so a fresh init and an upgrade converge.
ALTER TABLE commodore.artifact_creation_intents
    ADD COLUMN IF NOT EXISTS command_ack_leased_until TIMESTAMP;
