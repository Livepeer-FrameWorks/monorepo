-- Durable keyset cursor for jobs.reconcileFreezePublicationLedger. That sweep SKIPS a ledger row whose attempt
-- is still on the artifact (retrying); without a cursor it re-selects the same oldest batch every pass, so a
-- full batch of still-active attempts would starve every newer orphan from ever being inspected. This single-
-- row cursor advances by object_key (the PK) past every reviewed row each pass and wraps on a short page, so
-- skipped-because-retrying rows never block later rows and are re-checked on the next cycle.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
CREATE TABLE IF NOT EXISTS foghorn.freeze_publication_ledger_cursor (
    id       BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),  -- single-row guard
    last_key TEXT NOT NULL DEFAULT ''
);
INSERT INTO foghorn.freeze_publication_ledger_cursor (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
