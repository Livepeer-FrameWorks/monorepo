-- Durable, idempotent artifact CREATION intents. Written BEFORE the cross-service
-- Foghorn create so a crash or a lost/ambiguous create response leaves a
-- recoverable record instead of one plane silently missing. Keyed by the same
-- identity Foghorn dedups on — (tenant_id, kind, artifact_hash) — so re-inserting
-- the same intent is a no-op and a retried create cannot fork a second saga. The
-- convergence sweep drains 'pending' rows: it asks Foghorn for the actual state and
-- never treats an ambiguous RPC error as a rejection.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same table in the
-- baseline so a fresh init and an upgrade converge.

CREATE TABLE IF NOT EXISTS commodore.artifact_creation_intents (
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    request_id UUID NOT NULL,
    origin_cluster_id VARCHAR(100),
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    payload JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, kind, artifact_hash)
);

CREATE INDEX IF NOT EXISTS idx_commodore_creation_intents_pending
    ON commodore.artifact_creation_intents(updated_at)
    WHERE status = 'pending';
