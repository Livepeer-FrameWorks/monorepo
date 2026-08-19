ALTER TABLE periscope.billing_cursors
    ADD COLUMN IF NOT EXISTS source_id VARCHAR(128) NOT NULL DEFAULT 'periscope-default';

ALTER TABLE periscope.billing_cursors
    DROP CONSTRAINT IF EXISTS billing_cursors_pkey;

ALTER TABLE periscope.billing_cursors
    ADD PRIMARY KEY (source_id, tenant_id);

CREATE TABLE IF NOT EXISTS periscope.metering_leases (
    source_id VARCHAR(128) NOT NULL,
    partition_key VARCHAR(128) NOT NULL,
    owner_id VARCHAR(128) NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 1,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, partition_key)
);

CREATE TABLE IF NOT EXISTS periscope.metering_sources (
    source_id VARCHAR(128) PRIMARY KEY,
	source_region VARCHAR(128) NOT NULL DEFAULT '',
	activated_at TIMESTAMPTZ NOT NULL,
	completed_through TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE periscope.metering_sources
    ADD COLUMN IF NOT EXISTS completed_through TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS periscope.metering_reservation_keys (
    source_id VARCHAR(128) NOT NULL,
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL,
    last_sequence BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, tenant_id, cluster_id)
);
