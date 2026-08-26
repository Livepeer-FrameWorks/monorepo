ALTER TABLE quartermaster.tenant_cluster_access
    ALTER COLUMN expires_at TYPE TIMESTAMPTZ
    USING expires_at AT TIME ZONE 'UTC';
