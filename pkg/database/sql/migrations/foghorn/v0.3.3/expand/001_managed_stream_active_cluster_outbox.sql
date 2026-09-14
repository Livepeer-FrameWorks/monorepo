CREATE TABLE IF NOT EXISTS foghorn.managed_stream_active_cluster_outbox (
    id BIGSERIAL PRIMARY KEY,
    stream_id UUID NOT NULL UNIQUE,
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    desired_active BOOLEAN NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(100),
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_foghorn_managed_stream_active_cluster_outbox_due
    ON foghorn.managed_stream_active_cluster_outbox(next_attempt_at, id);
