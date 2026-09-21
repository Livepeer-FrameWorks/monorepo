-- Bosun: outbound webhook delivery. Tenant webhook endpoints and their
-- signing secrets, the public domain events received for delivery, one
-- delivery per (endpoint, event), every HTTP attempt, the email outbox for
-- automatically disabled endpoints, and the domain event outbox for audit
-- events. Every table carries tenant_id and every tenant-owned row is keyed by
-- it, so a lookup by ID alone cannot cross tenants.

CREATE SCHEMA IF NOT EXISTS bosun;

-- One row per tenant that has created an endpoint. endpoint_count is changed
-- in the transaction that creates or deletes an endpoint; the row lock
-- serializes concurrent creates and the check caps a tenant at 10 endpoints.
CREATE TABLE IF NOT EXISTS bosun.webhook_tenants (
    tenant_id UUID PRIMARY KEY,
    endpoint_count INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_webhook_tenants_endpoint_count CHECK (endpoint_count BETWEEN 0 AND 10)
);

-- A tenant's webhook endpoint. event_types lists the public event types the
-- endpoint receives; '*' receives every public type. api_version pins the
-- public event package major the payloads are rendered with. A disabled
-- endpoint receives no new deliveries and its pending deliveries are skipped;
-- disabled_reason is 'user' or 'failing' (automatic after sustained failure).
-- consecutive_failures and failing_since describe the current failure streak,
-- reset by any successful delivery. last_test_at rate-limits test deliveries.
CREATE TABLE IF NOT EXISTS bosun.webhook_endpoints (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    url TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    event_types TEXT[] NOT NULL,
    api_version TEXT NOT NULL DEFAULT 'v1',
    status TEXT NOT NULL DEFAULT 'enabled',
    disabled_reason TEXT,
    disabled_at TIMESTAMPTZ,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    failing_since TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_test_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_endpoints PRIMARY KEY (tenant_id, id),
    CONSTRAINT uq_webhook_endpoints_id UNIQUE (id),
    CONSTRAINT chk_webhook_endpoints_status CHECK (status IN ('enabled', 'disabled')),
    CONSTRAINT chk_webhook_endpoints_disabled CHECK ((status = 'disabled') = (disabled_reason IS NOT NULL AND disabled_at IS NOT NULL)),
    CONSTRAINT chk_webhook_endpoints_disabled_reason CHECK (disabled_reason IS NULL OR disabled_reason IN ('user', 'failing')),
    CONSTRAINT chk_webhook_endpoints_api_version CHECK (api_version IN ('v1')),
    CONSTRAINT chk_webhook_endpoints_event_types CHECK (cardinality(event_types) > 0)
);

CREATE INDEX IF NOT EXISTS idx_webhook_endpoints_tenant_created
    ON bosun.webhook_endpoints (tenant_id, created_at, id);

-- Signing secrets of an endpoint, field-encrypted. Each endpoint has exactly
-- one active secret. A rotation turns the active secret into 'previous' with
-- an expiry, during which deliveries carry a signature with both.
CREATE TABLE IF NOT EXISTS bosun.webhook_endpoint_secrets (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    endpoint_id UUID NOT NULL,
    secret_ciphertext TEXT NOT NULL,
    state TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_endpoint_secrets PRIMARY KEY (tenant_id, id),
    CONSTRAINT fk_webhook_endpoint_secrets_endpoint FOREIGN KEY (tenant_id, endpoint_id)
        REFERENCES bosun.webhook_endpoints (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_webhook_endpoint_secrets_state CHECK (state IN ('active', 'previous')),
    CONSTRAINT chk_webhook_endpoint_secrets_expiry CHECK ((state = 'previous') = (expires_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_endpoint_secrets_active
    ON bosun.webhook_endpoint_secrets (tenant_id, endpoint_id)
    WHERE state = 'active';

-- A public domain event received from domain.events. event_id is the
-- emitter's ce_id: the same event read from the local topic and from mirrored
-- copies inserts once. payload is the protobuf message and schema_name its full
-- proto name; delivery renders it as JSON. received_at bounds retention.
CREATE TABLE IF NOT EXISTS bosun.webhook_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    schema_name TEXT NOT NULL,
    subject TEXT NOT NULL,
    payload BYTEA NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_events PRIMARY KEY (tenant_id, event_id),
    CONSTRAINT uq_webhook_events_event_id UNIQUE (event_id)
);

CREATE INDEX IF NOT EXISTS idx_webhook_events_received
    ON bosun.webhook_events (received_at);

-- One delivery of one event to one endpoint, or a test delivery (kind 'test',
-- no event). status 'pending' covers the first attempt and every retry;
-- 'failed' is terminal after the retry schedule or a failed test; 'skipped'
-- is set when the endpoint is disabled. A worker owns a pending delivery while
-- lease_token is set and leased_until is in the future; settlement matches the
-- token, so a worker whose lease was reclaimed cannot settle it. A replay
-- returns a delivery to 'pending' under the same ID, so the receiver sees the
-- same webhook-id.
CREATE TABLE IF NOT EXISTS bosun.webhook_deliveries (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    endpoint_id UUID NOT NULL,
    event_id UUID,
    event_type TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'event',
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token UUID,
    leased_until TIMESTAMPTZ,
    last_status_code INTEGER,
    last_error_class TEXT,
    delivered_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    replay_count INTEGER NOT NULL DEFAULT 0,
    last_replayed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_deliveries PRIMARY KEY (tenant_id, id),
    CONSTRAINT uq_webhook_deliveries_id UNIQUE (id),
    CONSTRAINT fk_webhook_deliveries_endpoint FOREIGN KEY (tenant_id, endpoint_id)
        REFERENCES bosun.webhook_endpoints (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_webhook_deliveries_event FOREIGN KEY (tenant_id, event_id)
        REFERENCES bosun.webhook_events (tenant_id, event_id) ON DELETE CASCADE,
    CONSTRAINT chk_webhook_deliveries_kind CHECK (kind IN ('event', 'test')),
    CONSTRAINT chk_webhook_deliveries_kind_event CHECK ((kind = 'event') = (event_id IS NOT NULL)),
    CONSTRAINT chk_webhook_deliveries_status CHECK (status IN ('pending', 'succeeded', 'failed', 'skipped')),
    CONSTRAINT chk_webhook_deliveries_lease CHECK ((lease_token IS NULL) = (leased_until IS NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_deliveries_endpoint_event
    ON bosun.webhook_deliveries (endpoint_id, event_id)
    WHERE event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due
    ON bosun.webhook_deliveries (next_attempt_at, id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_leased
    ON bosun.webhook_deliveries (endpoint_id, leased_until)
    WHERE status = 'pending' AND lease_token IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_tenant_created
    ON bosun.webhook_deliveries (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint_created
    ON bosun.webhook_deliveries (tenant_id, endpoint_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_test_created
    ON bosun.webhook_deliveries (created_at)
    WHERE kind = 'test';

-- One HTTP attempt of a delivery. status_code is 0 when no response arrived;
-- error_class names the failure; response_excerpt holds at most 1 KiB of the
-- response body.
CREATE TABLE IF NOT EXISTS bosun.webhook_delivery_attempts (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    delivery_id UUID NOT NULL,
    attempt_number INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    error_class TEXT NOT NULL DEFAULT '',
    latency_ms INTEGER NOT NULL DEFAULT 0,
    response_excerpt TEXT NOT NULL DEFAULT '',
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_delivery_attempts PRIMARY KEY (tenant_id, id),
    CONSTRAINT fk_webhook_delivery_attempts_delivery FOREIGN KEY (tenant_id, delivery_id)
        REFERENCES bosun.webhook_deliveries (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_webhook_delivery_attempts_excerpt CHECK (octet_length(response_excerpt) <= 1024)
);

CREATE INDEX IF NOT EXISTS idx_webhook_delivery_attempts_delivery
    ON bosun.webhook_delivery_attempts (tenant_id, delivery_id, attempted_at);

-- Email to the tenant's billing contact when an endpoint is disabled
-- automatically, written in the disabling transaction and sent by a leased
-- worker.
CREATE TABLE IF NOT EXISTS bosun.webhook_notification_outbox (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    endpoint_id UUID NOT NULL,
    kind TEXT NOT NULL,
    endpoint_url TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_webhook_notification_outbox PRIMARY KEY (tenant_id, id),
    CONSTRAINT uq_webhook_notification_outbox_id UNIQUE (id),
    CONSTRAINT chk_webhook_notification_outbox_kind CHECK (kind IN ('endpoint_disabled'))
);

CREATE INDEX IF NOT EXISTS idx_webhook_notification_outbox_pending
    ON bosun.webhook_notification_outbox (next_attempt_at, id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_webhook_notification_outbox_completed
    ON bosun.webhook_notification_outbox (completed_at)
    WHERE completed_at IS NOT NULL;

-- Domain event outbox (pkg/events/outbox.TableDDL("bosun")).
CREATE TABLE IF NOT EXISTS bosun.domain_event_outbox (
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
    CONSTRAINT chk_bosun_domain_event_outbox_scope CHECK (scope IN ('tenant', 'platform')),
    CONSTRAINT chk_bosun_domain_event_outbox_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_bosun_domain_event_outbox_pending
    ON bosun.domain_event_outbox (enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bosun_domain_event_outbox_aggregate
    ON bosun.domain_event_outbox (aggregate_type, aggregate_id, enqueued_at, event_id)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_bosun_domain_event_outbox_completed
    ON bosun.domain_event_outbox (completed_at)
    WHERE completed_at IS NOT NULL;

-- Schema baseline identity marker. Records that this database was created from the
-- consolidated baseline at this floor, so the migration min-version guard treats
-- below-floor migrations as folded into the baseline (not missing). An existing
-- cluster upgraded in place has no marker and is checked for ledger completeness
-- instead. The floor value is kept in sync with provisioner.schemaMigrationBaselineFloor
-- by TestBaselineMarkerFloorMatchesConst. See docs/standards/schema-migrations.md.
CREATE TABLE IF NOT EXISTS public._schema_baseline (
    floor TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
    SELECT 'v0.3.0' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
