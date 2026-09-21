-- Domain event outbox (pkg/events/outbox.TableDDL("commodore")) and the
-- legacy service event outbox columns that dual-writing needs: event_id is
-- the ID shared with the domain row, lease_token fences settlement. Rows
-- written by the previous release have no event_id; their dispatch uses the
-- row ID.

CREATE TABLE IF NOT EXISTS commodore.domain_event_outbox (
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
    CONSTRAINT chk_commodore_domain_event_outbox_scope CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT chk_commodore_domain_event_outbox_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_commodore_domain_event_outbox_pending
    ON commodore.domain_event_outbox (enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_domain_event_outbox_aggregate
    ON commodore.domain_event_outbox (aggregate_type, aggregate_id, enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_domain_event_outbox_completed
    ON commodore.domain_event_outbox (completed_at)
    WHERE completed_at IS NOT NULL;

ALTER TABLE commodore.service_event_outbox
    ADD COLUMN IF NOT EXISTS event_id UUID,
    ADD COLUMN IF NOT EXISTS lease_token UUID;
