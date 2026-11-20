ALTER TABLE foghorn.processing_jobs
 ADD COLUMN IF NOT EXISTS required_memory_bytes BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS capacity_wait_started_at TIMESTAMP;
ALTER TABLE foghorn.processing_jobs DROP CONSTRAINT IF EXISTS chk_processing_required_memory;
ALTER TABLE foghorn.processing_jobs ADD CONSTRAINT chk_processing_required_memory CHECK (required_memory_bytes >= 0) NOT VALID;
