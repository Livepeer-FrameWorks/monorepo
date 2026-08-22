package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

type ServiceCatalogRow struct {
	ID, Name                                                             string
	ServiceID, Plane, Description, HealthCheckPath, DockerImage, Version sql.NullString
	DefaultPort                                                          sql.NullInt32
	Dependencies                                                         []string
	Tags                                                                 []byte
	IsActive                                                             bool
	Type, Protocol                                                       sql.NullString
	CreatedAt, UpdatedAt                                                 time.Time
}

func (q *Queries) ListServiceCatalog(ctx context.Context) ([]ServiceCatalogRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, service_id, name, plane, description, default_port,
		       health_check_path, docker_image, version, dependencies,
		       tags, is_active, type, protocol, created_at, updated_at
		FROM quartermaster.services
		WHERE COALESCE(plane, '') <> 'infra'
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ServiceCatalogRow
	for rows.Next() {
		var row ServiceCatalogRow
		if err := rows.Scan(&row.ID, &row.ServiceID, &row.Name, &row.Plane, &row.Description, &row.DefaultPort,
			&row.HealthCheckPath, &row.DockerImage, &row.Version, database.ArrayScan(&row.Dependencies),
			&row.Tags, &row.IsActive, &row.Type, &row.Protocol, &row.CreatedAt, &row.UpdatedAt); err != nil {
			// Catalog enumeration intentionally tolerates individual legacy rows.
			continue
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type ClusterServiceRow struct {
	ID, ClusterID, ServiceID, DesiredState string
	DesiredReplicas, CurrentReplicas       int32
	ConfigBlob, EnvironmentVars            sql.NullString
	CPULimit                               sql.NullFloat64
	MemoryLimitMB                          sql.NullInt32
	HealthStatus                           sql.NullString
	LastDeployed                           sql.NullTime
	CreatedAt, UpdatedAt                   time.Time
	ServiceName, ServicePlane              sql.NullString
}

func (q *Queries) ListClusterServiceAssignments(ctx context.Context, clusterID string) ([]ClusterServiceRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT cs.id, cs.cluster_id, cs.service_id, cs.desired_state, cs.desired_replicas,
		       cs.current_replicas, cs.config_blob, cs.environment_vars,
		       cs.cpu_limit, cs.memory_limit_mb, cs.health_status, cs.last_deployed,
		       cs.created_at, cs.updated_at, s.name as service_name, s.plane as service_plane
		FROM quartermaster.cluster_services cs
		LEFT JOIN quartermaster.services s ON s.service_id = cs.service_id
		WHERE cs.cluster_id = $1
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClusterServiceRow
	for rows.Next() {
		var row ClusterServiceRow
		if err := rows.Scan(&row.ID, &row.ClusterID, &row.ServiceID, &row.DesiredState, &row.DesiredReplicas,
			&row.CurrentReplicas, &row.ConfigBlob, &row.EnvironmentVars, &row.CPULimit, &row.MemoryLimitMB,
			&row.HealthStatus, &row.LastDeployed, &row.CreatedAt, &row.UpdatedAt, &row.ServiceName, &row.ServicePlane); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type UpsertTLSBundleParams struct {
	BundleID, ClusterID, Issuer, Email string
	DomainsJSON, MetadataJSON          *string
}
type UpsertedResourceRow struct {
	ID                   string
	CreatedAt, UpdatedAt time.Time
}

func (q *Queries) UpsertTLSBundle(ctx context.Context, arg UpsertTLSBundleParams) (UpsertedResourceRow, error) {
	var out UpsertedResourceRow
	err := q.db.QueryRowContext(ctx, `
		INSERT INTO quartermaster.tls_bundles (bundle_id, cluster_id, domains, issuer, email, metadata, updated_at)
		VALUES ($1, $2, COALESCE($3, '[]')::jsonb, $4, $5, COALESCE($6, '{}')::jsonb, NOW())
		ON CONFLICT (bundle_id) DO UPDATE SET cluster_id = EXCLUDED.cluster_id, domains = EXCLUDED.domains,
			issuer = EXCLUDED.issuer, email = EXCLUDED.email, metadata = EXCLUDED.metadata, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, arg.BundleID, arg.ClusterID, arg.DomainsJSON, arg.Issuer, arg.Email, arg.MetadataJSON).Scan(&out.ID, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

type UpsertIngressSiteParams struct {
	SiteID, ClusterID, NodeID, TLSBundleID, Kind, Upstream string
	DomainsJSON, MetadataJSON                              *string
}

func (q *Queries) UpsertIngressSite(ctx context.Context, arg UpsertIngressSiteParams) (UpsertedResourceRow, error) {
	var out UpsertedResourceRow
	err := q.db.QueryRowContext(ctx, `
		INSERT INTO quartermaster.ingress_sites (site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, metadata, updated_at)
		VALUES ($1, $2, $3, COALESCE($4, '[]')::jsonb, $5, $6, $7, COALESCE($8, '{}')::jsonb, NOW())
		ON CONFLICT (site_id) DO UPDATE SET cluster_id = EXCLUDED.cluster_id, node_id = EXCLUDED.node_id,
			domains = EXCLUDED.domains, tls_bundle_id = EXCLUDED.tls_bundle_id, kind = EXCLUDED.kind,
			upstream = EXCLUDED.upstream, metadata = EXCLUDED.metadata, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, arg.SiteID, arg.ClusterID, arg.NodeID, arg.DomainsJSON, arg.TLSBundleID, arg.Kind, arg.Upstream, arg.MetadataJSON).Scan(&out.ID, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

type ServiceHealthRow struct {
	InstanceID, ServiceID, ClusterID, Protocol string
	Host, HealthEndpoint                       sql.NullString
	Port                                       int32
	Status                                     string
	LastHealthCheck                            sql.NullTime
}

func (q *Queries) ListServiceHealth(ctx context.Context, serviceID string) ([]ServiceHealthRow, error) {
	where := "WHERE 1=1"
	args := []any{}
	if serviceID != "" {
		where = "WHERE service_id = $1"
		args = append(args, serviceID)
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT instance_id, service_id, cluster_id, protocol, advertise_host, port, health_endpoint_override, health_status, last_health_check
		FROM quartermaster.service_instances %s ORDER BY service_id, instance_id
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ServiceHealthRow
	for rows.Next() {
		var row ServiceHealthRow
		if err := rows.Scan(&row.InstanceID, &row.ServiceID, &row.ClusterID, &row.Protocol, &row.Host,
			&row.Port, &row.HealthEndpoint, &row.Status, &row.LastHealthCheck); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type ResourcePageFilter struct {
	ClusterID, NodeID string
	CursorTime        *time.Time
	CursorID          string
	Backward          bool
	Limit             int
}
type TLSBundleListRow struct {
	ID, BundleID, ClusterID string
	Domains, Metadata       []byte
	Issuer, Email           string
	CreatedAt, UpdatedAt    time.Time
}

func (q *Queries) ListTLSBundlesPage(ctx context.Context, filter ResourcePageFilter) ([]TLSBundleListRow, int32, error) {
	where, args := "WHERE 1=1", []any{}
	if filter.ClusterID != "" {
		args = append(args, filter.ClusterID)
		where += fmt.Sprintf(" AND cluster_id = $%d", len(args))
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.tls_bundles %s", where), args...).Scan(&total); err != nil {
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
		where += fmt.Sprintf(" AND (created_at, id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`SELECT id, bundle_id, cluster_id, domains, issuer, email, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM quartermaster.tls_bundles %s ORDER BY created_at %s, id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []TLSBundleListRow
	for rows.Next() {
		var row TLSBundleListRow
		if err := rows.Scan(&row.ID, &row.BundleID, &row.ClusterID, &row.Domains, &row.Issuer, &row.Email, &row.Metadata, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type IngressSiteListRow struct {
	ID, SiteID, ClusterID, NodeID string
	Domains                       []byte
	TLSBundleID, Kind, Upstream   string
	Metadata                      []byte
	CreatedAt, UpdatedAt          time.Time
}

func (q *Queries) ListIngressSitesPage(ctx context.Context, filter ResourcePageFilter) ([]IngressSiteListRow, int32, error) {
	where, args := "WHERE 1=1", []any{}
	if filter.ClusterID != "" {
		args = append(args, filter.ClusterID)
		where += fmt.Sprintf(" AND cluster_id = $%d", len(args))
	}
	if filter.NodeID != "" {
		args = append(args, filter.NodeID)
		where += fmt.Sprintf(" AND node_id = $%d", len(args))
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.ingress_sites %s", where), args...).Scan(&total); err != nil {
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
		where += fmt.Sprintf(" AND (created_at, id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`SELECT id, site_id, cluster_id, node_id, domains, tls_bundle_id, kind, upstream, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM quartermaster.ingress_sites %s ORDER BY created_at %s, id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []IngressSiteListRow
	for rows.Next() {
		var row IngressSiteListRow
		if err := rows.Scan(&row.ID, &row.SiteID, &row.ClusterID, &row.NodeID, &row.Domains, &row.TLSBundleID, &row.Kind, &row.Upstream, &row.Metadata, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type ServiceInstancePageFilter struct {
	ClusterID, ServiceID, NodeID string
	CursorTime                   *time.Time
	CursorID                     string
	Backward                     bool
	Limit                        int
}
type ServiceInstanceListRow struct {
	ID, InstanceID, ServiceID, ClusterID  string
	NodeID                                sql.NullString
	Protocol                              string
	AdvertiseHost                         sql.NullString
	Port                                  sql.NullInt32
	HealthEndpoint, Version               sql.NullString
	ProcessID                             sql.NullInt32
	ContainerID                           sql.NullString
	Status, HealthStatus                  string
	Metadata                              []byte
	StartedAt, StoppedAt, LastHealthCheck sql.NullTime
	CreatedAt, UpdatedAt                  time.Time
}

func (q *Queries) ListServiceInstancesPage(ctx context.Context, filter ServiceInstancePageFilter) ([]ServiceInstanceListRow, int32, error) {
	where, args := "WHERE COALESCE(s.plane, '') <> 'infra'", []any{}
	for _, item := range []struct{ column, value string }{{"si.cluster_id", filter.ClusterID}, {"si.service_id", filter.ServiceID}, {"si.node_id", filter.NodeID}} {
		if item.value != "" {
			args = append(args, item.value)
			where += fmt.Sprintf(" AND %s = $%d", item.column, len(args))
		}
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM quartermaster.service_instances si JOIN quartermaster.services s ON s.service_id = si.service_id %s`, where), args...).Scan(&total); err != nil {
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
		where += fmt.Sprintf(" AND (si.created_at, si.id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`
		SELECT si.id, si.instance_id, si.service_id, si.cluster_id, si.node_id, si.protocol, si.advertise_host, si.port,
		       si.health_endpoint_override, si.version, si.process_id, si.container_id, si.status, si.health_status, COALESCE(si.metadata, '{}'::jsonb),
		       si.started_at, si.stopped_at, si.last_health_check, si.created_at, si.updated_at
		FROM quartermaster.service_instances si JOIN quartermaster.services s ON s.service_id = si.service_id
		%s ORDER BY si.created_at %s, si.id %s LIMIT $%d
	`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []ServiceInstanceListRow
	for rows.Next() {
		var row ServiceInstanceListRow
		if err := rows.Scan(&row.ID, &row.InstanceID, &row.ServiceID, &row.ClusterID, &row.NodeID,
			&row.Protocol, &row.AdvertiseHost, &row.Port, &row.HealthEndpoint, &row.Version, &row.ProcessID, &row.ContainerID,
			&row.Status, &row.HealthStatus, &row.Metadata, &row.StartedAt, &row.StoppedAt, &row.LastHealthCheck, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type PhysicalServiceInstanceFilter struct {
	ServiceType, ClusterID string
	StaleThreshold         int32
}
type PhysicalServiceInstanceRow struct {
	InstanceID, ServiceID, ClusterID, NodeID string
	ExternalIP                               sql.NullString
	Status, HealthStatus                     string
	Port                                     int32
	Protocol                                 string
}

func (q *Queries) ListPhysicalServiceInstances(ctx context.Context, filter PhysicalServiceInstanceFilter) ([]PhysicalServiceInstanceRow, error) {
	where, args := "WHERE s.type = $1 AND si.status IN ('running','active') AND si.health_status = 'healthy' AND si.node_id IS NOT NULL AND n.external_ip IS NOT NULL AND n.status = 'active'", []any{filter.ServiceType}
	if filter.ClusterID != "" {
		args = append(args, filter.ClusterID)
		where += fmt.Sprintf(" AND si.cluster_id = $%d", len(args))
	}
	if filter.StaleThreshold > 0 {
		args = append(args, filter.StaleThreshold)
		where += fmt.Sprintf(" AND si.last_health_check > NOW() - ($%d * INTERVAL '1 second')", len(args))
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT si.instance_id, si.service_id, si.cluster_id, si.node_id, host(n.external_ip), si.status, si.health_status, si.port, si.protocol
		FROM quartermaster.service_instances si JOIN quartermaster.services s ON s.service_id = si.service_id
		JOIN quartermaster.infrastructure_nodes n ON si.node_id = n.node_id AND si.cluster_id = n.cluster_id
		%s ORDER BY si.node_id
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PhysicalServiceInstanceRow
	for rows.Next() {
		var row PhysicalServiceInstanceRow
		if err := rows.Scan(&row.InstanceID, &row.ServiceID, &row.ClusterID, &row.NodeID, &row.ExternalIP, &row.Status, &row.HealthStatus, &row.Port, &row.Protocol); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (q *Queries) ListProvisionedPhysicalIngressDomains(ctx context.Context) ([][]byte, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT si.domains FROM quartermaster.ingress_sites si
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
		WHERE si.kind = 'physical' AND n.status = 'active' AND n.external_ip IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][]byte
	for rows.Next() {
		var domains []byte
		if err := rows.Scan(&domains); err != nil {
			return nil, err
		}
		result = append(result, domains)
	}
	return result, rows.Err()
}
