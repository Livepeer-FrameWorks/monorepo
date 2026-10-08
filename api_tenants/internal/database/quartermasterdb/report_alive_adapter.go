package quartermasterdb

import (
	"context"
	"time"

	"github.com/lib/pq"
)

type PriorNodeState struct {
	NodeID, ClusterID, ExternalIP string
}

func (q *Queries) ListPriorNodeStates(ctx context.Context, nodeIDs []string) ([]PriorNodeState, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT node_id, cluster_id, COALESCE(host(external_ip), '')
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = ANY($1)
	`, pq.Array(nodeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PriorNodeState{}
	for rows.Next() {
		var row PriorNodeState
		if err := rows.Scan(&row.NodeID, &row.ClusterID, &row.ExternalIP); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type PriorEdgeInstanceState struct {
	NodeID, ServiceType, ClusterID, HealthStatus string
}

func (q *Queries) ListPriorEdgeInstanceStates(ctx context.Context, nodeIDs []string) ([]PriorEdgeInstanceState, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT si.node_id, svc.type, si.cluster_id, COALESCE(si.health_status, '')
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE si.node_id = ANY($1)
		  AND (svc.type = 'edge' OR svc.type LIKE 'edge-%')
		  AND si.instance_id = 'edge-cap-' || si.node_id || '-' || svc.type
	`, pq.Array(nodeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PriorEdgeInstanceState{}
	for rows.Next() {
		var row PriorEdgeInstanceState
		if err := rows.Scan(&row.NodeID, &row.ServiceType, &row.ClusterID, &row.HealthStatus); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type UpdateReportedNodesParams struct {
	NodeIDs, ExternalIPs []string
	RefreshHeartbeat     []bool
}

func (q *Queries) UpdateReportedNodes(ctx context.Context, arg UpdateReportedNodesParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes n
		SET last_heartbeat = CASE WHEN payload.refresh_hb THEN NOW() ELSE n.last_heartbeat END,
		    updated_at = NOW(),
		    external_ip = COALESCE(NULLIF(payload.ip, '')::inet, n.external_ip)
		FROM unnest($1::text[], $2::text[], $3::boolean[]) AS payload(node_id, ip, refresh_hb)
		WHERE n.node_id = payload.node_id
	`, pq.Array(arg.NodeIDs), pq.Array(arg.ExternalIPs), pq.Array(arg.RefreshHeartbeat))
	return err
}

type UpsertHealthyEdgeInstanceParams struct {
	InstanceID, NodeID, ServiceType string
	// RefreshAfterSeconds is how old last_health_check may grow before an
	// otherwise unchanged healthy row is rewritten to keep it fresh.
	RefreshAfterSeconds int32
}

// UpsertHealthyEdgeInstance records a node's edge capability as healthy. An
// existing row is rewritten only when a reported field differs or its check
// is RefreshAfterSeconds old, so repeated reports of an unchanged node leave
// the row alone.
func (q *Queries) UpsertHealthyEdgeInstance(ctx context.Context, arg UpsertHealthyEdgeInstanceParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_instances AS si
			(instance_id, cluster_id, node_id, service_id, protocol,
			 advertise_host, port, status, health_status, started_at,
			 last_health_check, created_at, updated_at)
		SELECT $1::varchar(100), n.cluster_id, $2::varchar(100), $3::varchar(100), 'http',
		       COALESCE(host(n.external_ip), ''), 18008, 'running', 'healthy',
		       COALESCE((SELECT started_at FROM quartermaster.service_instances WHERE instance_id = $1::varchar(100)), NOW()),
		       NOW(), NOW(), NOW()
		FROM quartermaster.infrastructure_nodes n
		WHERE n.node_id = $2::varchar(100)
		  AND n.status = 'active'
		  AND n.node_type = 'edge'
		ON CONFLICT (instance_id) DO UPDATE
		SET node_id = EXCLUDED.node_id,
		    service_id = EXCLUDED.service_id,
		    health_status = 'healthy',
		    status = 'running',
		    advertise_host = EXCLUDED.advertise_host,
		    last_health_check = NOW(),
		    updated_at = NOW()
		WHERE si.node_id IS DISTINCT FROM EXCLUDED.node_id
		   OR si.service_id IS DISTINCT FROM EXCLUDED.service_id
		   OR si.health_status IS DISTINCT FROM 'healthy'
		   OR si.status IS DISTINCT FROM 'running'
		   OR si.advertise_host IS DISTINCT FROM EXCLUDED.advertise_host
		   OR si.last_health_check IS NULL
		   OR si.last_health_check <= NOW() - ($4::int * INTERVAL '1 second')
	`, arg.InstanceID, arg.NodeID, arg.ServiceType, arg.RefreshAfterSeconds)
	return err
}

type MarkEdgeInstanceUnhealthyParams struct{ ServiceType, NodeID string }

// MarkEdgeInstanceUnhealthy marks a node's edge capability row unhealthy. A
// row that is already unhealthy is left alone: DNS publishes only fresh
// healthy rows, so an unhealthy row needs no freshness refresh.
func (q *Queries) MarkEdgeInstanceUnhealthy(ctx context.Context, arg MarkEdgeInstanceUnhealthyParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances si
		SET health_status = 'unhealthy',
		    last_health_check = NOW(),
		    updated_at = NOW()
		FROM quartermaster.services svc
		WHERE svc.service_id = si.service_id
		  AND svc.type = $1
		  AND si.node_id = $2
		  AND si.instance_id = 'edge-cap-' || si.node_id || '-' || svc.type
		  AND si.health_status IS DISTINCT FROM 'unhealthy'
	`, arg.ServiceType, arg.NodeID)
	return err
}

// HealthRefreshAfterSeconds is how old an unchanged health row's
// last_health_check may grow before a report rewrites it. DNS publishes a
// healthy row only while its check is younger than staleSeconds, and an
// unchanged row is next reconsidered up to one report gap later, so the row is
// rewritten while two full gaps remain: one lost or late report still leaves
// it fresh. When the stale bound leaves no such room, every report writes.
func HealthRefreshAfterSeconds(staleSeconds int, reportGap time.Duration) int32 {
	refresh := time.Duration(staleSeconds)*time.Second - 2*reportGap
	if refresh <= 0 {
		return 0
	}
	return int32(refresh / time.Second)
}
