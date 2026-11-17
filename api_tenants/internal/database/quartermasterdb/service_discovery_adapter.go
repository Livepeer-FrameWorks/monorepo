package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type DiscoveryScope string

const (
	DiscoveryScopeService DiscoveryScope = "service"
	DiscoveryScopeTenant  DiscoveryScope = "tenant"
	DiscoveryScopeDefault DiscoveryScope = "default"
)

type ServiceDiscoveryFilter struct {
	ServiceType, TenantID, ClusterID string
	Scope                            DiscoveryScope
	Pool, Physical                   bool
	StaleThreshold                   int32
	CursorTime                       *time.Time
	CursorID                         string
	Backward                         bool
	Limit                            int
}
type ServiceDiscoveryRow struct {
	ID, InstanceID, ServiceID, ClusterID string
	NodeID                               sql.NullString
	Protocol                             string
	AdvertiseHost                        sql.NullString
	Port                                 sql.NullInt32
	HealthEndpoint                       sql.NullString
	Status, HealthStatus                 string
	Metadata                             []byte
	LastHealthCheck                      sql.NullTime
	CreatedAt, UpdatedAt                 time.Time
	ClusterName, ClusterBaseURL          sql.NullString
	HealthFresh                          bool
}

func (q *Queries) DiscoverServicesPage(ctx context.Context, filter ServiceDiscoveryFilter) ([]ServiceDiscoveryRow, error) {
	clusterCol, extraJoin := "si.cluster_id", ""
	if filter.Pool {
		clusterCol = "sca.cluster_id"
		extraJoin = "JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = TRUE JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = sca.cluster_id"
	}
	metadataColumn := "COALESCE(si.metadata, '{}'::jsonb)"
	if filter.Physical && filter.ServiceType == "livepeer-gateway" {
		extraJoin += " LEFT JOIN quartermaster.infrastructure_clusters physical_cluster ON physical_cluster.cluster_id = si.cluster_id"
		// Pool assignment is not physical ownership. These reserved facts replace
		// registrant metadata and come only from canonical cluster inventory.
		metadataColumn = `(COALESCE(si.metadata, '{}'::jsonb) - 'physical_cluster_id' - 'auth_control_cell_id') || jsonb_build_object(
			'physical_cluster_id', COALESCE(physical_cluster.cluster_id, ''),
			'auth_control_cell_id', COALESCE(NULLIF(physical_cluster.control_cell_id, ''), NULLIF(physical_cluster.cell_id, ''), physical_cluster.cluster_id, ''))`
	}
	args := []any{filter.ServiceType}
	where := "WHERE s.type = $1 AND si.status IN ('running','starting','active')"
	if filter.ServiceType == "foghorn" {
		where += " AND si.protocol = 'grpc' AND " + foghornControlListenerFilter
	}
	switch filter.Scope {
	case DiscoveryScopeTenant:
		args = append(args, filter.TenantID)
		where += fmt.Sprintf(" AND (%s IN (SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca WHERE tca.tenant_id = $%d AND tca.is_active = true) OR %s IN (SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic WHERE ic.owner_tenant_id = $%d))", clusterCol, len(args), clusterCol, len(args))
	case DiscoveryScopeDefault:
		where += fmt.Sprintf(" AND %s IN (SELECT ic.cluster_id FROM quartermaster.infrastructure_clusters ic WHERE ic.is_default_cluster = true)", clusterCol)
	}
	if filter.ClusterID != "" {
		args = append(args, filter.ClusterID)
		where += fmt.Sprintf(" AND %s = $%d", clusterCol, len(args))
	}
	direction := "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (si.created_at, si.id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	selectClause := fmt.Sprintf(`si.id, si.instance_id, si.service_id, si.cluster_id, si.node_id, si.protocol, si.advertise_host, si.port, si.health_endpoint_override, si.status, si.health_status, %s, si.last_health_check, si.created_at, si.updated_at`, metadataColumn)
	if filter.Pool {
		selectClause = fmt.Sprintf(`si.id, si.instance_id, si.service_id, sca.cluster_id, si.node_id, si.protocol, si.advertise_host, si.port, si.health_endpoint_override, si.status, si.health_status, %s, si.last_health_check, si.created_at, si.updated_at, c.cluster_name, c.base_url`, metadataColumn)
	}
	if filter.Physical {
		selectClause += fmt.Sprintf(", (si.last_health_check IS NOT NULL AND si.last_health_check > NOW() - INTERVAL '%d seconds') AS health_fresh", filter.StaleThreshold)
	}
	query := fmt.Sprintf(`SELECT %s FROM quartermaster.service_instances si JOIN quartermaster.services s ON si.service_id = s.service_id %s %s ORDER BY si.created_at %s, si.id %s LIMIT $%d`, selectClause, extraJoin, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ServiceDiscoveryRow
	for rows.Next() {
		var row ServiceDiscoveryRow
		targets := []any{&row.ID, &row.InstanceID, &row.ServiceID, &row.ClusterID, &row.NodeID, &row.Protocol, &row.AdvertiseHost, &row.Port, &row.HealthEndpoint, &row.Status, &row.HealthStatus, &row.Metadata, &row.LastHealthCheck, &row.CreatedAt, &row.UpdatedAt}
		if filter.Pool {
			targets = append(targets, &row.ClusterName, &row.ClusterBaseURL)
		}
		if filter.Physical {
			targets = append(targets, &row.HealthFresh)
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
