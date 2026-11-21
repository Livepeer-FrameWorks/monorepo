ALTER TABLE foghorn.processing_jobs ADD COLUMN IF NOT EXISTS memory_rejected_nodes TEXT[] NOT NULL DEFAULT '{}';

CREATE SEQUENCE IF NOT EXISTS foghorn.processing_reservation_revision;
CREATE TABLE IF NOT EXISTS foghorn.processing_node_reservations (
 node_id VARCHAR(100) PRIMARY KEY,
 job_id UUID UNIQUE REFERENCES foghorn.processing_jobs(job_id) ON DELETE SET NULL,
 tenant_id UUID,
 required_memory_bytes BIGINT NOT NULL DEFAULT 0 CHECK (required_memory_bytes >= 0),
 revision BIGINT NOT NULL DEFAULT nextval('foghorn.processing_reservation_revision'),
 created_at TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

