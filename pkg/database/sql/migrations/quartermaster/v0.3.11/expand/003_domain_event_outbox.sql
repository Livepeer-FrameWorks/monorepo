-- v0.3.11: the domain event outbox (pkg/events/outbox.TableDDL) and a stable
-- event ID on legacy service events. A service event with a domain
-- counterpart carries the domain event's ID; rows written before this release
-- have no event_id and are dispatched under their row ID.

CREATE TABLE IF NOT EXISTS quartermaster.domain_event_outbox (
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
    CONSTRAINT chk_quartermaster_domain_event_outbox_scope CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT chk_quartermaster_domain_event_outbox_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_quartermaster_domain_event_outbox_pending
    ON quartermaster.domain_event_outbox (enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_quartermaster_domain_event_outbox_aggregate
    ON quartermaster.domain_event_outbox (aggregate_type, aggregate_id, enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_quartermaster_domain_event_outbox_completed
    ON quartermaster.domain_event_outbox (completed_at)
    WHERE completed_at IS NOT NULL;

ALTER TABLE quartermaster.service_event_outbox
    ADD COLUMN IF NOT EXISTS event_id UUID;
