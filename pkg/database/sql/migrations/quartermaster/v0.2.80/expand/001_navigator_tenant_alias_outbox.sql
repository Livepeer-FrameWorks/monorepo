-- Durable outbox for the platform subdomain-alias hand-off to Navigator.
-- Tenant create/rename, billing tier changes, and cluster-access changes all
-- enqueue rows in the same tx as the mutation, so a Navigator outage cannot
-- lose the intent. Rows are self-contained: the paid/active decision is made
-- at enqueue time, so the drain worker dispatches purely from stored fields.
--
-- seq (BIGSERIAL) is the monotonic enqueue order. Rows enqueued in the same
-- tx (retire(old) + ensure(new)) share an identical created_at, so the worker
-- serializes per tenant by seq, NOT created_at. The claim query dispatches at
-- most one in-flight row per tenant (no lower-seq incomplete row) so a newer
-- remove can never overtake an older ensure across replicas.
--
-- Schema source of truth: pkg/database/sql/schema/quartermaster.sql

CREATE TABLE IF NOT EXISTS quartermaster.navigator_tenant_alias_outbox (
    id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq BIGSERIAL NOT NULL,
    tenant_id  UUID NOT NULL,
    subdomain  TEXT,
    cluster_id TEXT,
    reason     TEXT,
    action     TEXT NOT NULL CHECK (action IN ('ensure', 'retire', 'remove', 'remove_cluster')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at   TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    completed_at TIMESTAMPTZ,
    CONSTRAINT chk_alias_outbox_subdomain CHECK (action NOT IN ('ensure', 'retire') OR NULLIF(btrim(subdomain), '') IS NOT NULL),
    CONSTRAINT chk_alias_outbox_cluster   CHECK (action <> 'remove_cluster' OR NULLIF(btrim(cluster_id), '') IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_qm_navigator_tenant_alias_outbox_pending
    ON quartermaster.navigator_tenant_alias_outbox(tenant_id, seq)
    WHERE completed_at IS NULL;
