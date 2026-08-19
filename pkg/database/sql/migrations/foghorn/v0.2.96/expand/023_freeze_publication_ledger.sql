-- Durable publication-attempt ledger. S3 publication (promote staging → attempt-versioned candidate) is NOT
-- transactional with Postgres, and SyncComplete is processed asynchronously with no client redelivery, so a
-- completion that PUBLISHES a candidate and then fails its guarded-CAS transaction (DB error, or a lost CAS
-- whose winner already cleared the attempt identity) could leave an unreferenced candidate with no cleanup row
-- and no retry. This ledger records EVERY object an in-flight attempt will produce — its staging upload
-- (guarded=false: always garbage once superseded) and its published candidate (guarded=true: garbage ONLY when
-- it is not the live active_object_key/active_dtsh_key) — BEFORE the object is promoted. A completion that
-- COMMITS deletes its own rows in the same transaction (the candidate is live, the staging is enqueued); rows
-- that survive are reconciled by jobs.reconcileFreezePublicationLedger, which is req-aware (an attempt whose
-- request id is STILL on the row is retrying and is never cleaned) so a retry can never race the sweep into
-- deleting an object it is about to make live.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.
CREATE TABLE IF NOT EXISTS foghorn.freeze_publication_ledger (
    object_key    TEXT PRIMARY KEY,
    artifact_hash TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    request_id    TEXT NOT NULL,   -- the attempt that produced this object; the sweep skips rows whose attempt is still on the artifact
    guarded       BOOLEAN NOT NULL, -- true = candidate (clean only if not the live pointer); false = staging (always garbage)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_foghorn_freeze_pub_ledger_age ON foghorn.freeze_publication_ledger(created_at);
