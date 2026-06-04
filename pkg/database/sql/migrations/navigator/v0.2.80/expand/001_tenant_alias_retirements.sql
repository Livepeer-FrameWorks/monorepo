-- Retired alias labels awaiting Bunny record cleanup. navigator.tenant_aliases
-- is keyed by tenant_id and overwrites subdomain in place on a rename, so it
-- cannot remember the old label. Quartermaster enqueues a retirement via
-- Navigator.RemoveTenantAliasSubdomain for the old label; the alias worker
-- clears that label's apex + per-service records and deletes the row.
-- requested_at lets the worker drop a stale retirement when the label was
-- re-pointed back to the tenant (the a -> b -> a case).
--
-- Schema source of truth: pkg/database/sql/schema/navigator.sql

CREATE TABLE IF NOT EXISTS navigator.tenant_alias_retirements (
    tenant_id    UUID NOT NULL,
    subdomain    TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    PRIMARY KEY (tenant_id, subdomain)
);
