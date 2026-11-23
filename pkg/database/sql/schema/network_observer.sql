CREATE SCHEMA IF NOT EXISTS network_observer;
CREATE TABLE IF NOT EXISTS network_observer.scheduler_lock (
 tenant_id UUID PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS network_observer.resources (
 tenant_id UUID NOT NULL,
 id TEXT NOT NULL,
 document JSONB NOT NULL,
 PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS network_observer.endpoints (
 tenant_id UUID NOT NULL,
 id TEXT NOT NULL,
 document JSONB NOT NULL,
 PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS network_observer.observers (
 tenant_id UUID NOT NULL,
 id TEXT NOT NULL,
 document JSONB NOT NULL,
 PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS network_observer.assignments (
 tenant_id UUID NOT NULL,
 id TEXT NOT NULL,
 document JSONB NOT NULL,
 PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS network_observer.ingest_receipts (
 tenant_id UUID NOT NULL,
 id UUID NOT NULL,
 digest TEXT NOT NULL,
 received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS network_observer.telemetry_outbox (
 tenant_id UUID NOT NULL,
 id UUID NOT NULL,
 payload BYTEA NOT NULL,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 attempts INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id,id)
);
CREATE INDEX IF NOT EXISTS idx_network_observer_outbox_due ON network_observer.telemetry_outbox (tenant_id,next_attempt_at);

CREATE TABLE IF NOT EXISTS network_observer.inbox_limits (
 tenant_id UUID NOT NULL,
 observer_id TEXT NOT NULL,
 pending_records BIGINT NOT NULL DEFAULT 0 CHECK (pending_records >= 0),
 pending_bytes BIGINT NOT NULL DEFAULT 0 CHECK (pending_bytes >= 0),
 last_processed_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
 PRIMARY KEY (tenant_id,observer_id)
);
CREATE TABLE IF NOT EXISTS network_observer.observation_inbox (
 tenant_id UUID NOT NULL,
 id UUID NOT NULL,
 observer_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('prober','gateway')),
 payload BYTEA,
 status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','rejected')),
 error_code TEXT NOT NULL DEFAULT '',
 received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 completed_at TIMESTAMPTZ,
 PRIMARY KEY (tenant_id,id),
 CHECK (status!='pending' OR payload IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_network_observer_inbox_pending ON network_observer.observation_inbox(tenant_id,observer_id,received_at,id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS idx_network_observer_inbox_completed ON network_observer.observation_inbox(tenant_id,completed_at) WHERE status!='pending';

CREATE TABLE IF NOT EXISTS public._schema_baseline (
 floor TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
 SELECT 'v0.3.0' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
