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
