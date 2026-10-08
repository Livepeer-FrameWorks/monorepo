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

CREATE TABLE IF NOT EXISTS public._schema_baseline (
 floor TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public._schema_baseline (floor)
 SELECT 'v0.3.0' WHERE NOT EXISTS (SELECT 1 FROM public._schema_baseline);
