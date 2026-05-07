-- Lifecycle audit log for signing-key create/revoke. No key material here.
-- Per-use observability stays in signing_keys.last_used_at + Foghorn metrics;
-- emitting one row per successful JWT verification would scale poorly under
-- viewer load (10K viewers per stream → 10K writes per surge).
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.signing_key_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    kid VARCHAR(64) NOT NULL,
    action TEXT NOT NULL,                 -- create | revoke
    actor_user_id UUID,
    actor_ip TEXT,
    detail TEXT,
    at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_key_audit_tenant_at
    ON commodore.signing_key_audit(tenant_id, at DESC);

CREATE INDEX IF NOT EXISTS idx_commodore_signing_key_audit_kid_at
    ON commodore.signing_key_audit(kid, at DESC);
