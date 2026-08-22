package quartermasterdb

import "context"

type ServedClustersForNodeParams struct{ Identity, ServiceType string }

func (q *Queries) ListServedClustersForNode(ctx context.Context, arg ServedClustersForNodeParams) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT DISTINCT sca.cluster_id
		FROM quartermaster.service_cluster_assignments sca
		JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.node_id = $1 AND svc.type = $2 AND sca.is_active = true
	`, arg.Identity, arg.ServiceType)
	return scanStrings(rows, err)
}

func scanStrings(rows rowsScanner, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

type rowsScanner interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}
