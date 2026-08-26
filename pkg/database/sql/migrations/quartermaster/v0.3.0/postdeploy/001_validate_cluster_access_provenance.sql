ALTER TABLE quartermaster.tenant_cluster_access
    VALIDATE CONSTRAINT chk_cluster_access_source;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM quartermaster.tenant_cluster_access access
        JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = access.cluster_id
        JOIN quartermaster.tenants tenant ON tenant.id = access.tenant_id
        WHERE access.access_source = 'unknown'
          AND (
              (cluster.is_platform_official = true AND tenant.official_cluster_id = access.cluster_id)
              OR cluster.owner_tenant_id = access.tenant_id
              OR COALESCE(access.invite_token, '') <> ''
          )
    ) THEN
        RAISE EXCEPTION 'derivable tenant cluster access provenance remains unknown';
    END IF;
END $$;
