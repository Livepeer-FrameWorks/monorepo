ALTER TABLE quartermaster.tenants
    ADD COLUMN IF NOT EXISTS provisioning_key VARCHAR(255);

CREATE UNIQUE INDEX IF NOT EXISTS idx_quartermaster_tenants_provisioning_key
    ON quartermaster.tenants(provisioning_key)
    WHERE provisioning_key IS NOT NULL;
