-- Realign the ack-drain index onto the next-due schedule. The released index
-- idx_commodore_creation_intents_ack_pending was on updated_at; the ack-drain now claims DUE
-- obligations ordered by command_ack_next_at, which that index cannot serve. The released
-- index shares this name, so a same-name CREATE IF NOT EXISTS in expand is inert on an
-- upgrade; the DROP+CREATE runs here in contract, where DROP is allowed, and the drop
-- precedes the plain recreate so an index always covers the claim for a completed upgrade.
-- IF EXISTS / IF NOT EXISTS keep the step reconcilable on re-apply.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.

DROP INDEX IF EXISTS commodore.idx_commodore_creation_intents_ack_pending;
CREATE INDEX IF NOT EXISTS idx_commodore_creation_intents_ack_pending
    ON commodore.artifact_creation_intents(command_ack_next_at)
    WHERE command_ack_pending = TRUE;
