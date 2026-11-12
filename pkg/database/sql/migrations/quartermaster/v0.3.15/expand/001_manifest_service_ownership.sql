ALTER TABLE quartermaster.service_instances
 ADD COLUMN IF NOT EXISTS registration_source TEXT NOT NULL DEFAULT 'legacy',
 ADD COLUMN IF NOT EXISTS registry_owner TEXT;
ALTER TABLE quartermaster.service_instances ALTER COLUMN registration_source SET DEFAULT 'runtime';
ALTER TABLE quartermaster.service_instances DROP CONSTRAINT IF EXISTS chk_service_instance_registration_source;
ALTER TABLE quartermaster.service_instances ADD CONSTRAINT chk_service_instance_registration_source
 CHECK (registration_source IN ('runtime','legacy','manifest') AND (registry_owner IS NULL OR (registration_source='manifest' AND btrim(registry_owner)<>''))) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_service_instances_manifest_owner
 ON quartermaster.service_instances(registry_owner ASC, cluster_id ASC, id ASC)
 WHERE registration_source = 'manifest';
