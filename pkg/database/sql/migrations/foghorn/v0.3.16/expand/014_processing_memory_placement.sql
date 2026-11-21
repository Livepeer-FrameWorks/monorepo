ALTER TABLE foghorn.processing_jobs
 ADD COLUMN IF NOT EXISTS required_memory_bytes BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS wait_reason TEXT,
 ADD COLUMN IF NOT EXISTS wait_started_at TIMESTAMP,
 ADD COLUMN IF NOT EXISTS source_probe JSONB,
 ADD COLUMN IF NOT EXISTS memory_reserved_bytes BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS memory_admitted_at TIMESTAMP;
ALTER TABLE foghorn.processing_jobs DROP CONSTRAINT IF EXISTS chk_processing_required_memory;
ALTER TABLE foghorn.processing_jobs ADD CONSTRAINT chk_processing_required_memory CHECK (required_memory_bytes >= 0 AND memory_reserved_bytes >= 0) NOT VALID;
ALTER TABLE foghorn.processing_jobs DROP CONSTRAINT IF EXISTS chk_processing_wait_reason;
ALTER TABLE foghorn.processing_jobs ADD CONSTRAINT chk_processing_wait_reason CHECK (wait_reason IS NULL OR wait_reason IN ('memory', 'slots', 'fitting_node_unavailable', 'cluster_unavailable')) NOT VALID;

CREATE OR REPLACE FUNCTION foghorn.reset_processing_memory_assignment() RETURNS trigger AS $$
BEGIN
 IF NEW.status IS DISTINCT FROM OLD.status AND NEW.status IN ('queued','dispatched','failed','completed') THEN
  NEW.memory_reserved_bytes := 0;
  NEW.memory_admitted_at := NULL;
 END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS processing_memory_assignment ON foghorn.processing_jobs;
CREATE TRIGGER processing_memory_assignment BEFORE UPDATE ON foghorn.processing_jobs
 FOR EACH ROW EXECUTE FUNCTION foghorn.reset_processing_memory_assignment();
