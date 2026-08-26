ALTER TABLE quartermaster.tenant_cluster_access
    ADD COLUMN IF NOT EXISTS access_source VARCHAR(50) NOT NULL DEFAULT 'unknown';

ALTER TABLE quartermaster.tenant_cluster_access
    DROP CONSTRAINT IF EXISTS chk_cluster_access_source;

ALTER TABLE quartermaster.tenant_cluster_access
    ADD CONSTRAINT chk_cluster_access_source CHECK (
        access_source IN (
            'unknown',
            'platform_tier',
            'owner',
            'private_invite',
            'marketplace_subscription',
            'operator_override'
        )
    ) NOT VALID;
