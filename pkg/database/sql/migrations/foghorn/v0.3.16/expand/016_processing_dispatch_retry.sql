ALTER TABLE foghorn.processing_jobs DROP CONSTRAINT IF EXISTS chk_processing_wait_reason;
ALTER TABLE foghorn.processing_jobs ADD CONSTRAINT chk_processing_wait_reason CHECK (wait_reason IS NULL OR wait_reason IN ('memory', 'slots', 'fitting_node_unavailable', 'cluster_unavailable', 'dispatch_retry')) NOT VALID;
