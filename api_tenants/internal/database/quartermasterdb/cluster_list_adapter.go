package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

type ClusterListScope string

const (
	ClusterScopeOwner          ClusterListScope = "owner"
	ClusterScopePlatform       ClusterListScope = "platform"
	ClusterScopePublicTopology ClusterListScope = "topology"
	ClusterScopeTenant         ClusterListScope = "tenant"
	ClusterScopeService        ClusterListScope = "service"
	ClusterScopeDefault        ClusterListScope = "default"
)

type ClusterListFilter struct {
	Scope                                                ClusterListScope
	ScopeID                                              string
	ClusterID, ClusterName, ClusterType, DeploymentModel string
	IsPlatformOfficial, PublicTopology                   *bool
	CursorTime                                           *time.Time
	CursorID                                             string
	Backward                                             bool
	Limit                                                int
}
type ClusterListRow struct {
	ID, ClusterID, ClusterName, ClusterType                                                 string
	OwnerTenantID                                                                           sql.NullString
	DeploymentModel, BaseURL                                                                string
	DatabaseURL, PeriscopeURL                                                               sql.NullString
	KafkaBrokers                                                                            []string
	MaxConcurrentStreams, MaxConcurrentViewers, MaxBandwidthMbps                            int32
	HealthStatus                                                                            string
	IsActive, IsDefaultCluster, IsPlatformOfficial, PublicTopology, AllowPrivatePullSources bool
	CreatedAt, UpdatedAt                                                                    time.Time
}

func (q *Queries) ListClustersPage(ctx context.Context, filter ClusterListFilter) ([]ClusterListRow, int32, error) {
	baseWhere, args := "WHERE c.is_default_cluster = true", []any{}
	switch filter.Scope {
	case ClusterScopeOwner:
		args = append(args, filter.ScopeID)
		baseWhere = "WHERE c.owner_tenant_id = $1"
	case ClusterScopePlatform:
		baseWhere = "WHERE c.is_platform_official = true AND c.is_active = true"
	case ClusterScopePublicTopology:
		baseWhere = "WHERE c.public_topology = true AND c.is_active = true"
	case ClusterScopeTenant:
		args = append(args, filter.ScopeID)
		baseWhere = "WHERE (c.cluster_id IN (SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca WHERE tca.tenant_id = $1 AND tca.is_active = true) OR c.owner_tenant_id = $1)"
	case ClusterScopeService:
		baseWhere = "WHERE c.is_active = true"
	case ClusterScopeDefault:
	}
	where := ""
	add := func(expression string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(expression, len(args))
	}
	if filter.ClusterID != "" {
		add(" AND c.cluster_id = $%d", filter.ClusterID)
	}
	if filter.ClusterName != "" {
		add(" AND c.cluster_name ILIKE '%%' || $%d || '%%'", filter.ClusterName)
	}
	if filter.ClusterType != "" {
		add(" AND c.cluster_type = $%d", filter.ClusterType)
	}
	if filter.DeploymentModel != "" {
		add(" AND c.deployment_model = $%d", filter.DeploymentModel)
	}
	if filter.IsPlatformOfficial != nil {
		add(" AND c.is_platform_official = $%d", *filter.IsPlatformOfficial)
	}
	if filter.PublicTopology != nil {
		add(" AND c.public_topology = $%d", *filter.PublicTopology)
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s %s", baseWhere, where), args...).Scan(&total); err != nil {
		return nil, 0, err
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
		where += fmt.Sprintf(" AND (c.created_at, c.id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`
		SELECT c.id, c.cluster_id, c.cluster_name, c.cluster_type, c.owner_tenant_id, c.deployment_model,
		       c.base_url, c.database_url, c.periscope_url, c.kafka_brokers, c.max_concurrent_streams,
		       c.max_concurrent_viewers, c.max_bandwidth_mbps, c.health_status, c.is_active, c.is_default_cluster,
		       c.is_platform_official, c.public_topology, c.allow_private_pull_sources, c.created_at, c.updated_at
		FROM quartermaster.infrastructure_clusters c %s %s ORDER BY c.created_at %s, c.id %s LIMIT $%d
	`, baseWhere, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []ClusterListRow
	for rows.Next() {
		var row ClusterListRow
		if err := rows.Scan(&row.ID, &row.ClusterID, &row.ClusterName, &row.ClusterType,
			&row.OwnerTenantID, &row.DeploymentModel, &row.BaseURL, &row.DatabaseURL, &row.PeriscopeURL, database.ArrayScan(&row.KafkaBrokers),
			&row.MaxConcurrentStreams, &row.MaxConcurrentViewers, &row.MaxBandwidthMbps, &row.HealthStatus, &row.IsActive,
			&row.IsDefaultCluster, &row.IsPlatformOfficial, &row.PublicTopology, &row.AllowPrivatePullSources, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type TenantClusterAccessRow struct {
	ClusterID, ClusterName, AccessLevel string
	ResourceLimits                      sql.NullString
	CreatedAt                           time.Time
	ID                                  string
}
type SimplePageFilter struct {
	ScopeID    string
	CursorTime *time.Time
	CursorID   string
	Backward   bool
	Limit      int
}

func (q *Queries) ListTenantClusterAccessPage(ctx context.Context, filter SimplePageFilter) ([]TenantClusterAccessRow, int32, error) {
	var total int32
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id WHERE a.tenant_id = $1 AND c.is_active = true`, filter.ScopeID).Scan(&total); err != nil {
		return nil, 0, err
	}
	where, args, direction := "WHERE a.tenant_id = $1 AND c.is_active = true", []any{filter.ScopeID}, "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (a.created_at, a.id) %s ($2, $3)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`SELECT c.cluster_id, c.cluster_name, a.access_level, a.resource_limits, a.created_at, a.id
		FROM quartermaster.infrastructure_clusters c JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id
		%s ORDER BY a.created_at %s, a.id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []TenantClusterAccessRow
	for rows.Next() {
		var row TenantClusterAccessRow
		if err := rows.Scan(&row.ClusterID, &row.ClusterName, &row.AccessLevel, &row.ResourceLimits, &row.CreatedAt, &row.ID); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type AvailableClusterRow struct {
	ClusterID, ClusterName, ClusterType string
	AutoEnroll                          bool
	CreatedAt                           time.Time
}

func (q *Queries) ListAvailableClustersPage(ctx context.Context, filter SimplePageFilter) ([]AvailableClusterRow, int32, error) {
	var total int32
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quartermaster.infrastructure_clusters WHERE is_active = true AND deployment_model = 'shared'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	where, args, direction := "WHERE is_active = true AND deployment_model = 'shared'", []any{}, "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (created_at, cluster_id) %s ($1, $2)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`SELECT cluster_id, cluster_name, cluster_type, true as auto_enroll, created_at
		FROM quartermaster.infrastructure_clusters %s ORDER BY created_at %s, cluster_id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []AvailableClusterRow
	for rows.Next() {
		var row AvailableClusterRow
		if err := rows.Scan(&row.ClusterID, &row.ClusterName, &row.ClusterType, &row.AutoEnroll, &row.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

func (q *Queries) ListSubscribedClustersPage(ctx context.Context, filter SimplePageFilter) ([]ClusterListRow, int32, error) {
	var total int32
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id IN (SELECT cluster_id FROM quartermaster.tenant_cluster_access WHERE tenant_id = $1 AND is_active = true)`, filter.ScopeID).Scan(&total); err != nil {
		return nil, 0, err
	}
	where, args, direction := `WHERE c.cluster_id IN (SELECT cluster_id FROM quartermaster.tenant_cluster_access WHERE tenant_id = $1 AND is_active = true)`, []any{filter.ScopeID}, "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (c.created_at, c.id) %s ($2, $3)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`
		SELECT c.id, c.cluster_id, c.cluster_name, c.cluster_type, c.owner_tenant_id, c.deployment_model,
		       c.base_url, c.database_url, c.periscope_url, c.kafka_brokers, c.max_concurrent_streams,
		       c.max_concurrent_viewers, c.max_bandwidth_mbps, c.health_status, c.is_active,
		       (c.cluster_id = COALESCE(t.primary_cluster_id, '') OR (t.primary_cluster_id IS NULL AND c.is_default_cluster)) AS is_default_cluster,
		       c.is_platform_official, c.public_topology, c.allow_private_pull_sources, c.created_at, c.updated_at
		FROM quartermaster.infrastructure_clusters c LEFT JOIN quartermaster.tenants t ON t.id = $1
		%s ORDER BY c.created_at %s, c.id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []ClusterListRow
	for rows.Next() {
		var row ClusterListRow
		if err := rows.Scan(&row.ID, &row.ClusterID, &row.ClusterName, &row.ClusterType,
			&row.OwnerTenantID, &row.DeploymentModel, &row.BaseURL, &row.DatabaseURL, &row.PeriscopeURL, database.ArrayScan(&row.KafkaBrokers),
			&row.MaxConcurrentStreams, &row.MaxConcurrentViewers, &row.MaxBandwidthMbps, &row.HealthStatus, &row.IsActive,
			&row.IsDefaultCluster, &row.IsPlatformOfficial, &row.PublicTopology, &row.AllowPrivatePullSources, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}
