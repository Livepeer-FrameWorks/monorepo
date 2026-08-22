package quartermasterdb

import (
	"context"
	"fmt"
	"strings"
)

type NodeStatusScope uint8

const (
	NodeStatusScopeActiveClusters NodeStatusScope = iota
	NodeStatusScopeTenantOwner
)

type UpdateNodeStatusParams struct {
	NodeID            string
	Status            string
	ExpectedClusterID string
	Scope             NodeStatusScope
	TenantID          string
}

type UpdateNodeStatusRow struct {
	NodeID    string
	ClusterID string
}

type UpdateNodeHardwareRecordParams struct {
	NodeID   string
	CpuCores *int32
	MemoryGB *int32
	DiskGB   *int32
}

func (q *Queries) UpdateNodeHardwareRecord(ctx context.Context, arg UpdateNodeHardwareRecordParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET cpu_cores = COALESCE($2, cpu_cores),
		    memory_gb = COALESCE($3, memory_gb),
		    disk_gb = COALESCE($4, disk_gb),
		    last_heartbeat = NOW(),
		    updated_at = NOW()
		WHERE node_id = $1`, arg.NodeID, arg.CpuCores, arg.MemoryGB, arg.DiskGB)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (q *Queries) UpdateNodeStatus(ctx context.Context, arg UpdateNodeStatusParams) (UpdateNodeStatusRow, error) {
	where := []string{"(n.node_id = $1 OR n.id::text = $1)"}
	args := []any{arg.NodeID, arg.Status}
	if arg.ExpectedClusterID != "" {
		args = append(args, arg.ExpectedClusterID)
		where = append(where, fmt.Sprintf("n.cluster_id = $%d", len(args)))
	}
	if arg.Scope == NodeStatusScopeTenantOwner {
		args = append(args, arg.TenantID)
		tenantArg := len(args)
		where = append(where, fmt.Sprintf(`n.cluster_id IN (
				SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
				WHERE c.owner_tenant_id = $%d AND c.is_active = true
				UNION
				SELECT tca.cluster_id FROM quartermaster.tenant_cluster_access tca
				WHERE tca.tenant_id = $%d
				  AND tca.access_level = 'owner'
				  AND tca.subscription_status = 'active'
				  AND tca.is_active = true
			)`, tenantArg, tenantArg))
	} else {
		where = append(where, `n.cluster_id IN (
			SELECT c.cluster_id FROM quartermaster.infrastructure_clusters c
			WHERE c.is_active = true
		)`)
	}
	query := fmt.Sprintf(`
		UPDATE quartermaster.infrastructure_nodes n
		SET status = $2, updated_at = NOW()
		WHERE %s
		RETURNING n.node_id, n.cluster_id
	`, strings.Join(where, " AND "))
	var row UpdateNodeStatusRow
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&row.NodeID, &row.ClusterID)
	return row, err
}
