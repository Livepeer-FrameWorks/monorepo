-- Lookout: incidents built from Alertmanager webhook notifications, their
-- alerts and timeline, the verified owner scope of each alerting cluster, and
-- the delivery outbox for operator notifications and Kafka.

CREATE SCHEMA IF NOT EXISTS lookout;

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- The incident scope of each alerting cluster, as last verified with
-- Quartermaster. Ingestion reads it under a share lock held until it commits.
-- Ownership reconciliation first commits the new owner, which waits for those
-- ingestions, and then moves the cluster's open incidents in a later
-- transaction whose snapshot includes them; under snapshot isolation a single
-- transaction would miss them. revision counts stored owner changes and
-- applied_revision the last one whose incident move committed, so a move
-- interrupted between the two transactions is finished later. verified is
-- false for a row created while Quartermaster could not answer: its platform
-- scope may open an incident but never moves one. source_updated_at is the
-- cluster's Quartermaster updated_at (NULL when Quartermaster reported the
-- cluster missing) and keeps an older answer from overwriting a newer one.
CREATE TABLE IF NOT EXISTS lookout.cluster_scopes (
    cluster_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    tenant_id UUID,
    verified BOOLEAN NOT NULL,
    source_updated_at TIMESTAMPTZ,
    revision BIGINT NOT NULL DEFAULT 0,
    applied_revision BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_cluster_scopes_scope CHECK (scope IN ('platform', 'tenant')),
    CONSTRAINT chk_cluster_scopes_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL)),
    CONSTRAINT chk_cluster_scopes_unverified_platform CHECK (verified OR scope = 'platform')
);

-- One row per incident. An Alertmanager group (groupKey) has at most one open
-- incident; once resolved, a later firing alert in the same group opens a new
-- row. Platform-scope incidents have no tenant; tenant-scope incidents belong
-- to the tenant that owns the alerting cluster.
CREATE TABLE IF NOT EXISTS lookout.incidents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scope TEXT NOT NULL,
    tenant_id UUID,
    cluster_id TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    group_key TEXT NOT NULL,
    alertname TEXT NOT NULL DEFAULT '',
    severity TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'firing',
    resolution TEXT,
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    last_alert_at TIMESTAMPTZ NOT NULL,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID,
    assigned_to UUID,
    resolved_at TIMESTAMPTZ,
    resolved_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_incidents_scope CHECK (scope IN ('platform', 'tenant')),
    CONSTRAINT chk_incidents_scope_tenant CHECK ((scope = 'tenant') = (tenant_id IS NOT NULL)),
    CONSTRAINT chk_incidents_status CHECK (status IN ('firing', 'acknowledged', 'resolved')),
    CONSTRAINT chk_incidents_resolution CHECK (
        (status = 'resolved' AND resolution IN ('auto', 'manual') AND resolved_at IS NOT NULL)
        OR (status <> 'resolved' AND resolution IS NULL AND resolved_at IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_incidents_open_group_key
    ON lookout.incidents (group_key) WHERE status <> 'resolved';

CREATE INDEX IF NOT EXISTS idx_incidents_tenant_created
    ON lookout.incidents (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_incidents_scope_created
    ON lookout.incidents (scope, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_incidents_group_key_created
    ON lookout.incidents (group_key, created_at DESC);

-- Latest Alertmanager state of each alert (by fingerprint) inside an incident.
-- ends_at is NULL while firing (Alertmanager sends the zero time).
CREATE TABLE IF NOT EXISTS lookout.incident_alerts (
    incident_id UUID NOT NULL REFERENCES lookout.incidents(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    status TEXT NOT NULL,
    alertname TEXT NOT NULL DEFAULT '',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    annotations JSONB NOT NULL DEFAULT '{}'::jsonb,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ,
    generator_url TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (incident_id, fingerprint),
    CONSTRAINT chk_incident_alerts_status CHECK (status IN ('firing', 'resolved'))
);

-- Resolved-incident lookups decide whether a firing alert is an Alertmanager
-- repeat (same fingerprint and startsAt) or a new firing.
CREATE INDEX IF NOT EXISTS idx_incident_alerts_fingerprint_start
    ON lookout.incident_alerts (fingerprint, starts_at);

-- Incident timeline. created_at defaults to clock_timestamp() so events
-- written in one transaction keep their insertion order.
CREATE TABLE IF NOT EXISTS lookout.incident_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID NOT NULL REFERENCES lookout.incidents(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    actor_user_id UUID,
    body JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT chk_incident_events_kind CHECK (kind IN (
        'alert_firing', 'alert_resolved', 'acknowledged', 'assigned', 'note',
        'resolved', 'investigation_attached', 'notified', 'scope_changed'
    ))
);

CREATE INDEX IF NOT EXISTS idx_incident_events_incident_created
    ON lookout.incident_events (incident_id, created_at, id);

-- Kafka redelivery to Skipper can attach the same report twice; one timeline
-- entry per (incident, report).
CREATE UNIQUE INDEX IF NOT EXISTS uq_incident_events_investigation_report
    ON lookout.incident_events (incident_id, (body->>'report_id'))
    WHERE kind = 'investigation_attached';

-- One row per (timeline event, channel). email/slack/discord rows carry
-- platform-scope operator notifications; kafka rows publish tenant incidents
-- to lookout.incidents (a rescoped incident re-arms its row for the new
-- tenant). Claims are fenced by lease_token so a worker whose lease expired
-- cannot settle a row another worker re-claimed. A row is settled once:
-- delivered_at on success, failed_at when an operator-channel row reaches the
-- delivery bound (kafka rows retry until delivered). Settled rows are deleted
-- by age.
CREATE TABLE IF NOT EXISTS lookout.notification_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES lookout.incident_events(id) ON DELETE CASCADE,
    incident_id UUID NOT NULL REFERENCES lookout.incidents(id) ON DELETE CASCADE,
    tenant_id UUID,
    channel TEXT NOT NULL,
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    lease_token UUID,
    last_error TEXT,
    delivered_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_notification_outbox_channel CHECK (channel IN ('email', 'slack', 'discord', 'kafka')),
    CONSTRAINT chk_notification_outbox_settled_once CHECK (delivered_at IS NULL OR failed_at IS NULL),
    CONSTRAINT uq_notification_outbox_event_channel UNIQUE (event_id, channel)
);

CREATE INDEX IF NOT EXISTS idx_notification_outbox_pending
    ON lookout.notification_outbox (next_attempt_at, created_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_notification_outbox_delivered
    ON lookout.notification_outbox (delivered_at)
    WHERE delivered_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_notification_outbox_failed
    ON lookout.notification_outbox (failed_at)
    WHERE failed_at IS NOT NULL;

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
