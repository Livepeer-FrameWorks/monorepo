-- command_ack_lease_token is the OWNERSHIP FENCE for the ack-drain claim, paired with the
-- command_ack_leased_until timestamp (expand/012). The claim stamps a fresh token it owns, and
-- every settlement (clear/backoff) CAS-matches it, so a worker whose lease was reclaimed by
-- another replica — its token replaced — mutates zero rows even if it resumes after the lease
-- expired. Without the token the timestamp lease alone cannot stop a stale worker from clearing
-- the new owner's lease or double-incrementing attempts once the lease has lapsed. A NULL token
-- (an obligation outstanding at upgrade, before this column existed) is only ever set by the next
-- claim, so no backfill is needed.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same column in the baseline
-- so a fresh init and an upgrade converge.
ALTER TABLE commodore.artifact_creation_intents
    ADD COLUMN IF NOT EXISTS command_ack_lease_token UUID;
