package quartermasterdb

import (
	"context"

	"github.com/lib/pq"
)

type ActivatedPrivateCluster struct {
	ClusterID, TenantID string
}

// ActivateReportedPrivateClusters completes initial readiness only for a
// healthy reported edge's persisted owning cluster. Explicit operator health
// states and inactive clusters/nodes must not be undone by a heartbeat.
func (q *Queries) ActivateReportedPrivateClusters(ctx context.Context, nodeIDs []string) ([]ActivatedPrivateCluster, error) {
	rows, err := q.db.QueryContext(ctx, `
		UPDATE quartermaster.infrastructure_clusters c
		SET health_status = 'healthy', updated_at = NOW()
		WHERE c.cluster_class = 'tenant_private'
		  AND c.cluster_type = 'edge'
		  AND c.health_status = 'unknown'
		  AND c.is_active = true
		  AND c.owner_tenant_id IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM quartermaster.tenants owner
		      WHERE owner.id = c.owner_tenant_id AND owner.is_active = true
		  )
		  AND EXISTS (
		      SELECT 1 FROM quartermaster.infrastructure_nodes n
		      WHERE n.node_id = ANY($1::text[])
		        AND n.cluster_id = c.cluster_id
		        AND n.node_type = 'edge' AND n.status = 'active'
		  )
		RETURNING c.cluster_id, c.owner_tenant_id::text
	`, pq.Array(nodeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var clusters []ActivatedPrivateCluster
	for rows.Next() {
		var cluster ActivatedPrivateCluster
		if err := rows.Scan(&cluster.ClusterID, &cluster.TenantID); err != nil {
			return nil, err
		}
		clusters = append(clusters, cluster)
	}
	return clusters, rows.Err()
}
