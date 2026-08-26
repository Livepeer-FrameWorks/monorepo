package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const ClusterAccessProvenanceID = "quartermaster_cluster_access_provenance_v0_3_0"

const insertMissingDerivedClusterAccessSQL = `
WITH candidates AS (
    SELECT tenant.id AS tenant_id, tenant.official_cluster_id AS cluster_id,
           'shared'::text AS access_level, 'platform_tier'::text AS access_source
    FROM quartermaster.tenants tenant
    JOIN quartermaster.infrastructure_clusters cluster
      ON cluster.cluster_id = tenant.official_cluster_id
     AND cluster.is_platform_official = true
     AND cluster.is_active = true
    LEFT JOIN quartermaster.tenant_cluster_access access
      ON access.tenant_id = tenant.id AND access.cluster_id = tenant.official_cluster_id
    WHERE tenant.is_active = true
      AND tenant.official_cluster_id IS NOT NULL
      AND access.id IS NULL
    UNION ALL
    SELECT cluster.owner_tenant_id, cluster.cluster_id, 'owner', 'owner'
    FROM quartermaster.infrastructure_clusters cluster
    JOIN quartermaster.tenants tenant ON tenant.id = cluster.owner_tenant_id AND tenant.is_active = true
    LEFT JOIN quartermaster.tenant_cluster_access access
      ON access.tenant_id = cluster.owner_tenant_id AND access.cluster_id = cluster.cluster_id
    WHERE cluster.owner_tenant_id IS NOT NULL
      AND cluster.is_active = true
      AND cluster.is_platform_official = false
      AND access.id IS NULL
    ORDER BY tenant_id, cluster_id
    LIMIT $1
)
INSERT INTO quartermaster.tenant_cluster_access (
    tenant_id, cluster_id, access_level, access_source, subscription_status,
    is_active, granted_at, created_at, updated_at
)
SELECT tenant_id, cluster_id, access_level, access_source, 'active',
       true, NOW(), NOW(), NOW()
FROM candidates
ON CONFLICT (tenant_id, cluster_id) DO NOTHING`

const clusterAccessProvenanceBatchSQL = `
WITH batch AS (
    SELECT access.tenant_id, access.cluster_id
    FROM quartermaster.tenant_cluster_access access
    JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = access.cluster_id
    JOIN quartermaster.tenants tenant ON tenant.id = access.tenant_id
    WHERE access.access_source = 'unknown'
      AND (
          (cluster.is_platform_official = true AND tenant.official_cluster_id = access.cluster_id)
          OR cluster.owner_tenant_id = access.tenant_id
          OR COALESCE(access.invite_token, '') <> ''
      )
    ORDER BY access.tenant_id, access.cluster_id
    LIMIT $1
    FOR UPDATE OF access SKIP LOCKED
), updated AS (
    UPDATE quartermaster.tenant_cluster_access access
    SET access_source = CASE
        WHEN cluster.is_platform_official = true AND tenant.official_cluster_id = access.cluster_id THEN 'platform_tier'
        WHEN cluster.owner_tenant_id = access.tenant_id THEN 'owner'
        ELSE 'private_invite'
    END
    FROM batch, quartermaster.infrastructure_clusters cluster, quartermaster.tenants tenant
    WHERE access.tenant_id = batch.tenant_id
      AND access.cluster_id = batch.cluster_id
      AND cluster.cluster_id = access.cluster_id
      AND tenant.id = access.tenant_id
    RETURNING access.access_source
)
SELECT
    (SELECT COUNT(*) FROM batch),
    (SELECT COUNT(*) FROM updated),
    0::bigint`

// Register installs Quartermaster's service-owned background migrations.
func Register() {
	datamigrate.Register(datamigrate.Migration{
		ID:                  ClusterAccessProvenanceID,
		Service:             "quartermaster",
		IntroducedIn:        "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "derive authoritative provenance for pre-v0.3 tenant cluster access grants",
		Run:                 runClusterAccessProvenance,
		Verify:              verifyClusterAccessProvenance,
	})
}

func runClusterAccessProvenance(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	result, err := db.ExecContext(ctx, insertMissingDerivedClusterAccessSQL, batchSize)
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("materialize missing derived cluster access: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("count materialized derived cluster access: %w", err)
	}

	// Unknown remains intentionally fail-closed when legacy state does not carry
	// enough evidence to distinguish an operator override from an invalid grant.
	var scanned, changed, unresolved int64
	if err := db.QueryRowContext(ctx, clusterAccessProvenanceBatchSQL, batchSize).Scan(&scanned, &changed, &unresolved); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill cluster access provenance: %w", err)
	}
	return datamigrate.Progress{
		Scanned: scanned,
		Changed: inserted + changed - unresolved,
		Skipped: unresolved,
		Done:    scanned < int64(batchSize) && inserted < int64(batchSize),
	}, nil
}

func verifyClusterAccessProvenance(ctx context.Context, db datamigrate.DB) error {
	var count int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM quartermaster.tenant_cluster_access access
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = access.cluster_id
JOIN quartermaster.tenants tenant ON tenant.id = access.tenant_id
WHERE access.access_source = 'unknown'
  AND (
      (cluster.is_platform_official = true AND tenant.official_cluster_id = access.cluster_id)
      OR cluster.owner_tenant_id = access.tenant_id
      OR COALESCE(access.invite_token, '') <> ''
  )`).Scan(&count); err != nil {
		return fmt.Errorf("verify cluster access provenance: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("cluster access provenance has %d derivable rows remaining", count)
	}
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM (
    SELECT tenant.id, tenant.official_cluster_id AS cluster_id
    FROM quartermaster.tenants tenant
    JOIN quartermaster.infrastructure_clusters cluster
      ON cluster.cluster_id = tenant.official_cluster_id
     AND cluster.is_platform_official = true
     AND cluster.is_active = true
    LEFT JOIN quartermaster.tenant_cluster_access access
      ON access.tenant_id = tenant.id AND access.cluster_id = tenant.official_cluster_id
    WHERE tenant.is_active = true AND tenant.official_cluster_id IS NOT NULL AND access.id IS NULL
    UNION ALL
    SELECT cluster.owner_tenant_id, cluster.cluster_id
    FROM quartermaster.infrastructure_clusters cluster
    JOIN quartermaster.tenants tenant ON tenant.id = cluster.owner_tenant_id AND tenant.is_active = true
    LEFT JOIN quartermaster.tenant_cluster_access access
      ON access.tenant_id = cluster.owner_tenant_id AND access.cluster_id = cluster.cluster_id
    WHERE cluster.owner_tenant_id IS NOT NULL AND cluster.is_active = true
      AND cluster.is_platform_official = false AND access.id IS NULL
) missing`).Scan(&count); err != nil {
		return fmt.Errorf("verify missing derived cluster access: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("cluster access provenance has %d derived grants missing", count)
	}
	return nil
}
