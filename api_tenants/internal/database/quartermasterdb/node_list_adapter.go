package quartermasterdb

import (
	"context"
	"fmt"
	"time"
)

type NodeListScope string

const (
	NodeScopeTenant  NodeListScope = "tenant"
	NodeScopeService NodeListScope = "service"
	NodeScopePublic  NodeListScope = "public"
)

type NodeListFilter struct {
	Scope                                 NodeListScope
	TenantID, ClusterID, NodeType, Region string
	CursorTime                            *time.Time
	CursorID                              string
	Backward                              bool
	Limit                                 int
}

func (q *Queries) ListInfrastructureNodesPage(ctx context.Context, filter NodeListFilter) ([]GetInfrastructureNodeRow, int32, error) {
	where, args := "WHERE n.cluster_id IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.public_topology = true AND c.is_active = true)", []any{}
	switch filter.Scope {
	case NodeScopeTenant:
		args = append(args, filter.TenantID)
		where = "WHERE n.cluster_id IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.owner_tenant_id = $1 AND c.is_active = true)"
	case NodeScopeService:
		where = "WHERE n.cluster_id IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.is_active = true)"
	}
	add := func(expression string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(expression, len(args))
	}
	if filter.ClusterID != "" {
		add(" AND n.cluster_id = $%d", filter.ClusterID)
	}
	if filter.NodeType != "" {
		add(" AND n.node_type = $%d", filter.NodeType)
	}
	if filter.Region != "" {
		add(" AND n.region = $%d", filter.Region)
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.infrastructure_nodes n %s", where), args...).Scan(&total); err != nil {
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
		where += fmt.Sprintf(" AND (n.created_at, n.id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	query := fmt.Sprintf(`SELECT n.id, n.node_id, n.cluster_id, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone,
		n.latitude, n.longitude, n.cpu_cores, n.memory_gb, n.disk_gb, n.last_heartbeat, n.enrollment_origin,
		n.applied_mesh_revision, n.status, n.created_at, n.updated_at,
		COALESCE((SELECT c.owner_tenant_id::text FROM quartermaster.infrastructure_clusters c WHERE c.cluster_id = n.cluster_id), '')::text,
		n.snapshot_cpu_percent, n.snapshot_ram_used_bytes, n.snapshot_ram_total_bytes, n.snapshot_disk_used_bytes,
		n.snapshot_disk_total_bytes, n.snapshot_uptime_seconds, n.snapshot_at
		FROM quartermaster.infrastructure_nodes n %s ORDER BY n.created_at %s, n.id %s LIMIT $%d`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []GetInfrastructureNodeRow
	for rows.Next() {
		var row GetInfrastructureNodeRow
		if err := rows.Scan(&row.ID, &row.NodeID, &row.ClusterID, &row.NodeName, &row.NodeType,
			&row.InternalIp, &row.ExternalIp, &row.WireguardIp, &row.WireguardPublicKey, &row.WireguardListenPort, &row.Region, &row.AvailabilityZone,
			&row.Latitude, &row.Longitude, &row.CpuCores, &row.MemoryGb, &row.DiskGb, &row.LastHeartbeat, &row.EnrollmentOrigin,
			&row.AppliedMeshRevision, &row.Status, &row.CreatedAt, &row.UpdatedAt, &row.OwnerTenantID, &row.SnapshotCpuPercent,
			&row.SnapshotRamUsedBytes, &row.SnapshotRamTotalBytes, &row.SnapshotDiskUsedBytes, &row.SnapshotDiskTotalBytes, &row.SnapshotUptimeSeconds, &row.SnapshotAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

type HealthyNodeFilter struct {
	Scope                            NodeListScope
	TenantID, ClusterID, ServiceType string
	StaleThreshold                   int32
	Assigned                         bool
}

func (q *Queries) ListHealthyServiceNodes(ctx context.Context, filter HealthyNodeFilter) ([]GetInfrastructureNodeRow, int32, int32, error) {
	clusterColumn := "n.cluster_id"
	if filter.Assigned {
		clusterColumn = "sca.cluster_id"
	}
	where, args := fmt.Sprintf("WHERE %s IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.public_topology = true AND c.is_active = true)", clusterColumn), []any{}
	switch filter.Scope {
	case NodeScopeTenant:
		args = append(args, filter.TenantID)
		where = fmt.Sprintf("WHERE %s IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.owner_tenant_id = $1 AND c.is_active = true)", clusterColumn)
	case NodeScopeService:
		where = fmt.Sprintf("WHERE %s IN (SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c WHERE c.is_active = true)", clusterColumn)
	}
	if filter.ClusterID != "" {
		args = append(args, filter.ClusterID)
		where += fmt.Sprintf(" AND %s = $%d", clusterColumn, len(args))
	}
	if filter.Assigned {
		args = append(args, filter.ServiceType)
		where += fmt.Sprintf(" AND sca.is_active = TRUE AND s.type = $%d AND n.external_ip IS NOT NULL AND n.status = 'active'", len(args))
	} else {
		if filter.ServiceType != "" {
			args = append(args, filter.ServiceType)
			where += fmt.Sprintf(" AND s.type = $%d", len(args))
		}
		where += " AND n.external_ip IS NOT NULL AND n.status = 'active'"
		if filter.ServiceType == "edge" || len(filter.ServiceType) > 5 && filter.ServiceType[:5] == "edge-" {
			where += " AND n.node_type = 'edge'"
		}
	}
	joins := `JOIN quartermaster.service_instances si ON si.cluster_id = n.cluster_id AND (si.node_id = n.node_id OR si.advertise_host = host(n.external_ip) OR si.advertise_host = host(n.internal_ip) OR si.advertise_host = host(n.wireguard_ip)) JOIN quartermaster.services s ON si.service_id = s.service_id`
	from := "quartermaster.infrastructure_nodes n " + joins
	candidateCluster := "n.cluster_id"
	if filter.Assigned {
		joins = `JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id JOIN quartermaster.services s ON si.service_id = s.service_id JOIN quartermaster.infrastructure_nodes n ON si.node_id = n.node_id OR si.advertise_host = host(n.external_ip) OR si.advertise_host = host(n.internal_ip) OR si.advertise_host = host(n.wireguard_ip)`
		from = "quartermaster.service_instances si " + joins
		candidateCluster = "sca.cluster_id"
	}
	// One statement returns every candidate (node, cluster) with its health, so
	// the total, the healthy count and the healthy rows come from one snapshot.
	// Separate statements let a concurrent health or lifecycle write land
	// between them and report candidates with no healthy node while one is
	// healthy, which DNS callers read as a real outage.
	args = append(args, filter.StaleThreshold)
	query := fmt.Sprintf(`WITH candidates AS (
		SELECT n.id AS node_pk, %[1]s AS cluster_key,
			COALESCE(bool_or(si.health_status = 'healthy' AND si.last_health_check > NOW() - ($%[2]d * INTERVAL '1 second')), FALSE) AS healthy
		FROM %[3]s %[4]s
		GROUP BY n.id, %[1]s
	)
	SELECT n.id, n.node_id, cand.cluster_key, n.node_name, n.node_type, n.internal_ip, n.external_ip,
		n.wireguard_ip, n.wireguard_public_key, n.wireguard_listen_port, n.region, n.availability_zone, n.latitude, n.longitude,
		n.cpu_cores, n.memory_gb, n.disk_gb, n.last_heartbeat, n.enrollment_origin, n.applied_mesh_revision, n.status, n.created_at, n.updated_at,
		COALESCE(oc.owner_tenant_id::text, '')::text,
		n.snapshot_cpu_percent, n.snapshot_ram_used_bytes, n.snapshot_ram_total_bytes, n.snapshot_disk_used_bytes,
		n.snapshot_disk_total_bytes, n.snapshot_uptime_seconds, n.snapshot_at, cand.healthy
	FROM candidates cand JOIN quartermaster.infrastructure_nodes n ON n.id = cand.node_pk
	-- A join, not a per-row subquery: on Yugabyte each correlated subquery
	-- execution is its own storage read.
	LEFT JOIN quartermaster.infrastructure_clusters oc ON oc.cluster_id = cand.cluster_key
	ORDER BY n.node_id, cand.cluster_key`, candidateCluster, len(args), from, where)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var result []GetInfrastructureNodeRow
	var total, healthy int32
	for rows.Next() {
		var row GetInfrastructureNodeRow
		var rowHealthy bool
		if err := rows.Scan(&row.ID, &row.NodeID, &row.ClusterID, &row.NodeName, &row.NodeType, &row.InternalIp,
			&row.ExternalIp, &row.WireguardIp, &row.WireguardPublicKey, &row.WireguardListenPort, &row.Region, &row.AvailabilityZone, &row.Latitude,
			&row.Longitude, &row.CpuCores, &row.MemoryGb, &row.DiskGb, &row.LastHeartbeat, &row.EnrollmentOrigin, &row.AppliedMeshRevision,
			&row.Status, &row.CreatedAt, &row.UpdatedAt, &row.OwnerTenantID, &row.SnapshotCpuPercent, &row.SnapshotRamUsedBytes,
			&row.SnapshotRamTotalBytes, &row.SnapshotDiskUsedBytes, &row.SnapshotDiskTotalBytes, &row.SnapshotUptimeSeconds, &row.SnapshotAt, &rowHealthy); err != nil {
			return nil, 0, 0, err
		}
		total++
		if rowHealthy {
			healthy++
			result = append(result, row)
		}
	}
	return result, total, healthy, rows.Err()
}
