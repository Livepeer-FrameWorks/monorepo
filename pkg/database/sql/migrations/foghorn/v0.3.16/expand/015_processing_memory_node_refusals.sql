ALTER TABLE foghorn.processing_jobs ADD COLUMN IF NOT EXISTS memory_rejected_nodes TEXT[] NOT NULL DEFAULT '{}';

CREATE SEQUENCE IF NOT EXISTS foghorn.processing_reservation_revision;
-- Node revision fences outlive jobs. History purge clears their owner and bytes
-- atomically; the fairness sweep also clears missing or terminal owners.
CREATE TABLE IF NOT EXISTS foghorn.processing_node_reservations (
 node_id VARCHAR(100) PRIMARY KEY,
 job_id UUID UNIQUE,
 tenant_id UUID,
 required_memory_bytes BIGINT NOT NULL DEFAULT 0 CHECK (required_memory_bytes >= 0),
 revision BIGINT NOT NULL DEFAULT nextval('foghorn.processing_reservation_revision'),
 created_at TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

