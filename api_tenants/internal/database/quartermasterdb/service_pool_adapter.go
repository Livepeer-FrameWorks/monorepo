package quartermasterdb

import (
	"context"
	"time"

	"github.com/lib/pq"
)

type ServicePoolStatusRow struct {
	ID, InstanceID, Host, Status, AssignedCluster string
	Port                                          int32
	CreatedAt                                     time.Time
}

func (q *Queries) ListServicePoolStatus(ctx context.Context, serviceType string) ([]ServicePoolStatusRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT si.id, si.instance_id, COALESCE(si.advertise_host, '') AS host,
		       COALESCE(si.port, 0) AS port, si.status, si.created_at,
		       COALESCE(sca.cluster_id, '') AS assigned_cluster
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		LEFT JOIN quartermaster.service_cluster_assignments sca
		  ON sca.service_instance_id = si.id AND sca.is_active = true
		WHERE svc.type = $1
		ORDER BY assigned_cluster, si.started_at ASC
	`, serviceType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ServicePoolStatusRow{}
	for rows.Next() {
		var row ServicePoolStatusRow
		if err := rows.Scan(&row.ID, &row.InstanceID, &row.Host, &row.Port, &row.Status, &row.CreatedAt, &row.AssignedCluster); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type ServicePoolInstancesParams struct {
	InstanceIDs []string
	ServiceType string
}

func (q *Queries) ReleaseServicePoolInstances(ctx context.Context, arg ServicePoolInstancesParams) ([]string, int64, error) {
	rows, err := q.db.QueryContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE service_instance_id IN (
			SELECT si.id FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.id = ANY($1) AND svc.type = $2
		)
		RETURNING cluster_id
	`, pq.Array(arg.InstanceIDs), arg.ServiceType)
	return collectDeletedClusters(rows, err)
}

type ReleaseOldestServicePoolParams struct {
	ClusterID   string
	Count       int32
	ServiceType string
}

func (q *Queries) ReleaseOldestServicePoolInstances(ctx context.Context, arg ReleaseOldestServicePoolParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE id IN (
			SELECT sca.id
			FROM quartermaster.service_cluster_assignments sca
			JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE svc.type = $3
			  AND sca.cluster_id = $1
			  AND sca.is_active = true
			  AND si.status = 'running'
			ORDER BY si.started_at ASC
			LIMIT $2
		)
	`, arg.ClusterID, arg.Count, arg.ServiceType)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type ServicePoolInstanceParams struct{ InstanceID, ServiceType string }

func (q *Queries) DrainServicePoolInstance(ctx context.Context, arg ServicePoolInstanceParams) ([]string, int64, error) {
	rows, err := q.db.QueryContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE service_instance_id = (
			SELECT si.id FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.id = $1 AND svc.type = $2
		)
		RETURNING cluster_id
	`, arg.InstanceID, arg.ServiceType)
	return collectDeletedClusters(rows, err)
}

func collectDeletedClusters(rows rowsScanner, err error) ([]string, int64, error) {
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []string{}
	seen := map[string]struct{}{}
	var count int64
	for rows.Next() {
		var clusterID string
		if err := rows.Scan(&clusterID); err != nil {
			return nil, 0, err
		}
		count++
		if _, ok := seen[clusterID]; !ok && clusterID != "" {
			seen[clusterID] = struct{}{}
			result = append(result, clusterID)
		}
	}
	return result, count, rows.Err()
}

func (q *Queries) ServicePoolClusterActive(ctx context.Context, clusterID string) (bool, error) {
	var exists bool
	err := q.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1 AND is_active = true)", clusterID).Scan(&exists)
	return exists, err
}

type AssignServicePoolInstanceParams struct{ ClusterID, InstanceID, ServiceType string }

func (q *Queries) AssignServicePoolInstance(ctx context.Context, arg AssignServicePoolInstanceParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, $1, 'runtime'
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.id = $2::uuid AND svc.type = $3 AND si.status = 'running'
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, arg.ClusterID, arg.InstanceID, arg.ServiceType)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type AssignServicePoolCountParams struct {
	ClusterID   string
	Count       int32
	ServiceType string
}

func (q *Queries) AssignServicePoolCount(ctx context.Context, arg AssignServicePoolCountParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, $1, 'runtime'
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		LEFT JOIN quartermaster.service_cluster_assignments sca
		  ON sca.service_instance_id = si.id AND sca.is_active = true
		WHERE svc.type = $3 AND si.status = 'running'
		GROUP BY si.id
		ORDER BY COUNT(sca.id) ASC, si.started_at ASC, si.id ASC
		LIMIT $2
		ON CONFLICT (service_instance_id, cluster_id) DO UPDATE SET is_active = true, updated_at = NOW()
	`, arg.ClusterID, arg.Count, arg.ServiceType)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type UnassignServicePoolParams struct {
	ClusterID   string
	InstanceIDs []string
	ServiceType string
}

func (q *Queries) UnassignServicePoolInstances(ctx context.Context, arg UnassignServicePoolParams) error {
	_, err := q.db.ExecContext(ctx, `
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE cluster_id = $1
		  AND service_instance_id IN (
			SELECT si.id FROM quartermaster.service_instances si
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.id = ANY($2::uuid[]) AND svc.type = $3
		  )
	`, arg.ClusterID, pq.Array(arg.InstanceIDs), arg.ServiceType)
	return err
}
