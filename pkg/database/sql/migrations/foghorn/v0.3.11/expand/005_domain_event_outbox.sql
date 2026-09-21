-- Transactional outbox for Foghorn's domain events (pkg/events/outbox). Rows are
-- written in the transaction that commits the artifact or ingest-session state
-- change they describe and relayed to Decklog PublishDomainEvents. The DDL is
-- outbox.TableDDL("foghorn") verbatim.
CREATE TABLE IF NOT EXISTS foghorn.domain_event_outbox (
    event_id          UUID PRIMARY KEY,
    event_type        TEXT NOT NULL,
    source            TEXT NOT NULL,
    aggregate_type    TEXT NOT NULL,
    aggregate_id      TEXT NOT NULL,
    aggregate_version BIGINT NOT NULL DEFAULT 0,
    scope             TEXT NOT NULL,
    tenant_id         UUID,
    actor_auth_type   TEXT NOT NULL DEFAULT '',
    actor_user_id     TEXT NOT NULL DEFAULT '',
    actor_token_hash  TEXT NOT NULL DEFAULT '',
    occurred_at       TIMESTAMPTZ NOT NULL,
    payload           BYTEA NOT NULL,
    enqueued_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    claimed_at        TIMESTAMPTZ,
    lease_token       UUID,
    attempts          INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    completed_at      TIMESTAMPTZ,
    CONSTRAINT chk_foghorn_domain_event_outbox_scope CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT chk_foghorn_domain_event_outbox_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_foghorn_domain_event_outbox_pending
    ON foghorn.domain_event_outbox (enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_foghorn_domain_event_outbox_aggregate
    ON foghorn.domain_event_outbox (aggregate_type, aggregate_id, enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_foghorn_domain_event_outbox_completed
    ON foghorn.domain_event_outbox (completed_at)
    WHERE completed_at IS NOT NULL;
