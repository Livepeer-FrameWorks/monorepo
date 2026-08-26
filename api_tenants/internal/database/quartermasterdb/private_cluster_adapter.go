package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type FoghornControlCellRow struct {
	InstanceID, ControlCellID, RegionID, BaseURL string
	Load                                         int
	Latitude, Longitude                          sql.NullFloat64
	StartedAt                                    sql.NullTime
}

func (q *Queries) ListFoghornControlCells(ctx context.Context, clusterID, regionID string, useRegion bool) ([]FoghornControlCellRow, error) {
	args := []any{}
	where := `
			WHERE svc.type = 'foghorn'
			  AND si.status = 'running'
			  AND si.health_status = 'healthy'
			  AND si.protocol = 'grpc'
			  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			  AND ic.cluster_class = 'platform_official'
			  AND ic.is_active = true`
	if clusterID != "" {
		args = append(args, clusterID)
		where += fmt.Sprintf("\n\t\t\t  AND ic.cluster_id = $%d", len(args))
	} else if useRegion && regionID != "" {
		args = append(args, regionID)
		where += fmt.Sprintf("\n\t\t\t  AND ic.region_id = $%d", len(args))
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT si.id::text AS instance_id, ic.cluster_id AS control_cell,
		       COALESCE(ic.region_id, '') AS control_region, COALESCE(ic.base_url, '') AS control_base_url,
		       COUNT(sca.id) AS load, n.latitude, n.longitude, si.started_at
		FROM quartermaster.service_instances si JOIN quartermaster.services svc ON svc.service_id = si.service_id
		JOIN quartermaster.service_cluster_assignments primary_sca ON primary_sca.service_instance_id = si.id AND primary_sca.is_active = true
		JOIN quartermaster.infrastructure_clusters ic ON ic.cluster_id = primary_sca.cluster_id
		LEFT JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true
		LEFT JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
		%s GROUP BY si.id, ic.cluster_id, ic.region_id, ic.base_url, n.latitude, n.longitude, si.started_at
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FoghornControlCellRow
	for rows.Next() {
		var row FoghornControlCellRow
		if err := rows.Scan(&row.InstanceID, &row.ControlCellID, &row.RegionID, &row.BaseURL, &row.Load, &row.Latitude, &row.Longitude, &row.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type CreatePrivateInfrastructureClusterParams struct {
	ID               string
	ClusterID        string
	ClusterName      string
	OwnerTenantID    string
	ShortDescription *string
	CreatedAt        time.Time
	ControlCellID    string
	RegionID         string
	BaseURL          string
}

// CreatePrivateInfrastructureCluster preserves the positional write contract
// shared by both private-cluster lifecycle RPCs.
func (q *Queries) CreatePrivateInfrastructureCluster(ctx context.Context, arg CreatePrivateInfrastructureClusterParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (
			id, cluster_id, cluster_name, cluster_type, deployment_model,
			owner_tenant_id, base_url,
			max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps,
			visibility, pricing_model, short_description,
			region_id, cell_id, cluster_class, control_cell_id, eligible_serving_cell_ids,
			health_status, is_active, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'edge', 'self-hosted',
			$4, $9,
			0, 0, 0,
			'private', 'free_unmetered', $5,
			NULLIF($8::text, ''), $2, 'tenant_private', $7::text, ARRAY[$7::text]::TEXT[],
			'unknown', true, $6, $6
		)
	`, arg.ID, arg.ClusterID, arg.ClusterName, arg.OwnerTenantID, arg.ShortDescription,
		arg.CreatedAt, arg.ControlCellID, arg.RegionID, arg.BaseURL)
	return err
}

type GrantPrivateClusterOwnerAccessParams struct {
	TenantID  string
	ClusterID string
}

func (q *Queries) GrantPrivateClusterOwnerAccess(ctx context.Context, arg GrantPrivateClusterOwnerAccessParams) error {
	_, err := q.db.ExecContext(ctx, `
			INSERT INTO quartermaster.tenant_cluster_access (
				tenant_id, cluster_id, access_level, access_source, subscription_status, is_active, created_at, updated_at
			) VALUES ($1, $2, 'owner', 'owner', 'active', true, NOW(), NOW())
	`, arg.TenantID, arg.ClusterID)
	return err
}

type AssignRuntimeFoghornToPrivateClusterParams struct {
	ServiceInstanceID string
	ClusterID         string
}

func (q *Queries) AssignRuntimeFoghornToPrivateCluster(ctx context.Context, arg AssignRuntimeFoghornToPrivateClusterParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, $2, 'runtime'
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.id = $1::uuid AND svc.type = 'foghorn'
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, arg.ServiceInstanceID, arg.ClusterID)
	return err
}

type AssignFoghornToPrivateClusterParams struct {
	ServiceInstanceID string
	ClusterID         string
}

func (q *Queries) AssignFoghornToPrivateCluster(ctx context.Context, arg AssignFoghornToPrivateClusterParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
		VALUES ($1::uuid, $2)
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, arg.ServiceInstanceID, arg.ClusterID)
	return err
}

type CreateEdgeBootstrapTokenRecordParams struct {
	ID          string
	TokenHash   string
	TokenPrefix string
	Name        string
	TenantID    string
	ClusterID   sql.NullString
	ExpiresAt   time.Time
}

func (q *Queries) CreateEdgeBootstrapTokenRecord(ctx context.Context, arg CreateEdgeBootstrapTokenRecordParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (
			id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
		) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
	`, arg.ID, arg.TokenHash, arg.TokenPrefix, arg.Name, arg.TenantID, arg.ClusterID, arg.ExpiresAt)
	return err
}
