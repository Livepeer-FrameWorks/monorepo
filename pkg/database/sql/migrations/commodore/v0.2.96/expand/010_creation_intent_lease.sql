-- Lease columns for the multi-replica creation-intent convergence sweep. A replica
-- CLAIMS a batch of pending intents by stamping a fresh lease_token + leased_until;
-- every terminal transition CAS-checks lease_token so two sweepers never both
-- terminalize the same intent. NULL/expired lease = claimable.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same columns in
-- the baseline so a fresh init and an upgrade converge.
ALTER TABLE commodore.artifact_creation_intents
    ADD COLUMN IF NOT EXISTS lease_token UUID,
    ADD COLUMN IF NOT EXISTS leased_until TIMESTAMP;
