-- Close commodore.artifact_creation_intents' half of the creation state machine: kind
-- and status are fixed domains, and request_id (the key the convergence sweep uses
-- against Foghorn's command ledger) is UNIQUE so a duplicated request_id cannot mint two
-- local intents that only one Foghorn ledger row matches. The PRIMARY KEY on
-- (tenant_id, kind, artifact_hash) stays the ON CONFLICT dedup target; UNIQUE(request_id)
-- is an orthogonal guard on the ledger key.
--
-- Postdeploy phase: these constraints validate existing rows on ADD, so they run after
-- new binaries roll forward (the expand/008 create + expand/009 lease columns already
-- exist). The intents table is small and its values already conform, so the validating
-- ADD is non-blocking in practice — and a UNIQUE constraint cannot be added NOT VALID,
-- which is why the whole set lives here rather than expand.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same constraints in
-- the baseline so a fresh init and an upgrade converge. Constraint names are explicit so
-- baseline and replay produce identical catalog entries. DROP IF EXISTS before each ADD
-- keeps the migration idempotent (it replays on top of the full baseline).
ALTER TABLE commodore.artifact_creation_intents
    DROP CONSTRAINT IF EXISTS artifact_creation_intents_kind_check;
ALTER TABLE commodore.artifact_creation_intents
    ADD CONSTRAINT artifact_creation_intents_kind_check CHECK (kind IN ('clip', 'dvr', 'vod'));

ALTER TABLE commodore.artifact_creation_intents
    DROP CONSTRAINT IF EXISTS artifact_creation_intents_status_check;
ALTER TABLE commodore.artifact_creation_intents
    ADD CONSTRAINT artifact_creation_intents_status_check CHECK (status IN ('pending', 'committed', 'aborted'));

-- origin_cluster_id keys Foghorn resolution for both convergence and the ack drain; an
-- empty/NULL one can never converge, so the create paths reject it in Go (upsertCreationIntent)
-- and this CHECK closes the same invariant at the database.
ALTER TABLE commodore.artifact_creation_intents
    DROP CONSTRAINT IF EXISTS artifact_creation_intents_origin_cluster_check;
ALTER TABLE commodore.artifact_creation_intents
    ADD CONSTRAINT artifact_creation_intents_origin_cluster_check CHECK (origin_cluster_id IS NOT NULL AND origin_cluster_id <> '');

ALTER TABLE commodore.artifact_creation_intents
    DROP CONSTRAINT IF EXISTS artifact_creation_intents_request_id_key;
ALTER TABLE commodore.artifact_creation_intents
    ADD CONSTRAINT artifact_creation_intents_request_id_key UNIQUE (request_id);
