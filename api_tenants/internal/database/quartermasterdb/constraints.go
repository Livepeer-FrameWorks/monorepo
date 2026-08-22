package quartermasterdb

import "context"

const deferBootstrapNodeClusterConstraints = `
SET CONSTRAINTS quartermaster.fk_qm_service_instances_node_cluster, quartermaster.fk_qm_ingress_sites_node_cluster DEFERRED
`

// DeferBootstrapNodeClusterConstraints keeps the node and its dependent rows
// movable within the bootstrap command's existing outer transaction.
func (q *Queries) DeferBootstrapNodeClusterConstraints(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, deferBootstrapNodeClusterConstraints)
	return err
}
