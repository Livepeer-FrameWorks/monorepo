-- Durable RETRY schedule for the creation-intent ack-drain worker. command_ack_attempts
-- counts prior non-discharging attempts and command_ack_next_at is the next-due time: when an
-- intent first sets command_ack_pending=TRUE it seeds attempts=0 and next_at=NOW() (due
-- immediately). The retry schedule is a SEPARATE axis from the claim lease
-- (command_ack_leased_until, added in 012): the drain claims a due, unleased batch by stamping
-- a fixed lease, processes it concurrently within that lease, and ONLY a non-discharging
-- outcome increments attempts and pushes next_at forward by a capped exponential (base 30s,
-- ceiling 15m). A terminal-consumed (or idempotent MISSING) ack clears the flag. A NULL next_at
-- (an obligation outstanding at upgrade, before this column existed) is treated as due and
-- sorted first by the claim query, so no data backfill is needed. The index that reads due rows
-- soonest-first is realigned in the contract phase.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same columns in the
-- baseline so a fresh init and an upgrade converge.
ALTER TABLE commodore.artifact_creation_intents
    ADD COLUMN IF NOT EXISTS command_ack_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS command_ack_next_at TIMESTAMP;
