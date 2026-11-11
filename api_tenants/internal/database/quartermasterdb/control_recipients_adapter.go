package quartermasterdb

import (
	"context"

	"github.com/lib/pq"
)

type TenantControlRecipientRow struct{ ClusterID, CellID string }

// ListTenantControlRecipients retains revoked access and caller-owned execution
// history. Health controls delivery progress, never the required recipient set.
func (q *Queries) ListTenantControlRecipients(ctx context.Context, tenantID string, historical []string) ([]TenantControlRecipientRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		WITH required AS (
			SELECT c.cluster_id
			FROM quartermaster.tenant_cluster_access access
			JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = access.cluster_id
			WHERE access.tenant_id = $1::uuid AND c.cluster_type = 'edge'
			UNION
			SELECT unnest($2::text[])
		)
		SELECT required.cluster_id,
		       CASE WHEN c.cluster_id IS NULL THEN ''
		            WHEN c.cluster_class = 'tenant_private' THEN COALESCE(NULLIF(c.control_cell_id, ''), '')
		            ELSE COALESCE(NULLIF(c.control_cell_id, ''), NULLIF(c.cell_id, ''), c.cluster_id)
		       END AS cell_id
		FROM required
		LEFT JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = required.cluster_id
		WHERE c.cluster_id IS NULL OR c.cluster_type = 'edge'
		ORDER BY required.cluster_id
	`, tenantID, pq.Array(historical))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TenantControlRecipientRow
	for rows.Next() {
		var row TenantControlRecipientRow
		if err := rows.Scan(&row.ClusterID, &row.CellID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
