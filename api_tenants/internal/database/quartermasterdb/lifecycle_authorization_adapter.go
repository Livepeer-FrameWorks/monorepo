package quartermasterdb

import "context"

func (q *Queries) TenantIsProvider(ctx context.Context, tenantID string) (bool, error) {
	var isProvider bool
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(is_provider, false)
		FROM quartermaster.tenants
		WHERE id = $1
	`, tenantID).Scan(&isProvider)
	return isProvider, err
}

func (q *Queries) ActiveClusterExists(ctx context.Context, clusterID string) (bool, error) {
	var exists bool
	err := q.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.infrastructure_clusters
			WHERE cluster_id = $1 AND is_active = true
		)
	`, clusterID).Scan(&exists)
	return exists, err
}

type TenantHasClusterLifecycleAccessParams struct {
	ClusterID string
	TenantID  string
}

func (q *Queries) TenantHasClusterLifecycleAccess(ctx context.Context, arg TenantHasClusterLifecycleAccessParams) (bool, error) {
	var exists bool
	err := q.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.infrastructure_clusters
			WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
			UNION
			SELECT 1 FROM quartermaster.tenant_cluster_access
			WHERE cluster_id = $1
			  AND tenant_id = $2
			  AND access_level = 'owner'
			  AND subscription_status = 'active'
			  AND is_active = true
		)
	`, arg.ClusterID, arg.TenantID).Scan(&exists)
	return exists, err
}
