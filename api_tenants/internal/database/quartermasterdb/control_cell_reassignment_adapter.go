package quartermasterdb

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// ClusterControlCellReassignment is the control-cell ownership record of one
// tenant-private cluster.
type ClusterControlCellReassignment struct {
	ClusterID             string
	OwnerTenantID         string
	ControlCellID         string
	PreviousControlCellID string
	State                 string // "", switching, failed
	StartedAt             sql.NullTime
	DeadlineAt            sql.NullTime
	Error                 string
}

const clusterControlCellReassignmentColumns = `
	cluster_id, COALESCE(owner_tenant_id::text, ''), COALESCE(control_cell_id, ''),
	COALESCE(previous_control_cell_id, ''), COALESCE(reassignment_state, ''),
	reassignment_started_at, reassignment_deadline_at, COALESCE(reassignment_error, '')`

type reassignmentScanner interface {
	Scan(dest ...any) error
}

func scanClusterControlCellReassignment(row reassignmentScanner) (ClusterControlCellReassignment, error) {
	var r ClusterControlCellReassignment
	err := row.Scan(&r.ClusterID, &r.OwnerTenantID, &r.ControlCellID, &r.PreviousControlCellID,
		&r.State, &r.StartedAt, &r.DeadlineAt, &r.Error)
	return r, err
}

// GetClusterControlCellReassignment returns the control-cell record of an
// active tenant-private cluster, or sql.ErrNoRows.
func (q *Queries) GetClusterControlCellReassignment(ctx context.Context, clusterID string) (ClusterControlCellReassignment, error) {
	return scanClusterControlCellReassignment(q.db.QueryRowContext(ctx, `
		SELECT `+clusterControlCellReassignmentColumns+`
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1 AND cluster_class = 'tenant_private' AND is_active = true
	`, clusterID))
}

// ControlCellHasRunningFoghorn reports whether cellID is an active platform
// cell with at least one running Foghorn.
func (q *Queries) ControlCellHasRunningFoghorn(ctx context.Context, cellID string) (bool, error) {
	var ok bool
	err := q.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM quartermaster.infrastructure_clusters cell
			JOIN quartermaster.service_cluster_assignments sca
			  ON sca.cluster_id = cell.cluster_id AND sca.is_active = true
			JOIN quartermaster.service_instances si
			  ON si.id = sca.service_instance_id AND si.status = 'running'
			JOIN quartermaster.services svc
			  ON svc.service_id = si.service_id AND svc.type = 'foghorn'
			WHERE cell.cluster_id = $1
			  AND cell.cluster_class = 'platform_official'
			  AND cell.is_active = true
		)
	`, cellID).Scan(&ok)
	return ok, err
}

type StartClusterControlCellReassignmentParams struct {
	ClusterID    string
	TargetCellID string
	DeadlineAt   time.Time
}

// StartClusterControlCellReassignment moves an idle or failed tenant-private
// cluster to the target control cell and opens a switching reassignment. It
// returns sql.ErrNoRows when the cluster is missing, already switching, or
// already controlled by the target.
func (q *Queries) StartClusterControlCellReassignment(ctx context.Context, arg StartClusterControlCellReassignmentParams) (ClusterControlCellReassignment, error) {
	return scanClusterControlCellReassignment(q.db.QueryRowContext(ctx, `
		UPDATE quartermaster.infrastructure_clusters
		SET previous_control_cell_id = control_cell_id,
		    control_cell_id = $2,
		    eligible_serving_cell_ids = array_replace(eligible_serving_cell_ids, control_cell_id::text, $2::text),
		    reassignment_state = 'switching',
		    reassignment_started_at = NOW(),
		    reassignment_deadline_at = $3,
		    reassignment_error = NULL,
		    updated_at = NOW()
		WHERE cluster_id = $1
		  AND cluster_class = 'tenant_private'
		  AND is_active = true
		  AND control_cell_id IS NOT NULL
		  AND control_cell_id <> $2
		  AND (reassignment_state IS NULL OR reassignment_state = 'failed')
		RETURNING `+clusterControlCellReassignmentColumns,
		arg.ClusterID, arg.TargetCellID, arg.DeadlineAt))
}

// ListSwitchingClusterControlCellReassignments returns every tenant-private
// cluster whose edges are moving to a new control cell.
func (q *Queries) ListSwitchingClusterControlCellReassignments(ctx context.Context) ([]ClusterControlCellReassignment, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+clusterControlCellReassignmentColumns+`
		FROM quartermaster.infrastructure_clusters
		WHERE reassignment_state = 'switching' AND cluster_class = 'tenant_private' AND is_active = true
		ORDER BY reassignment_started_at, cluster_id
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClusterControlCellReassignment
	for rows.Next() {
		r, scanErr := scanClusterControlCellReassignment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListControlCellPendingNodes returns the nodes of a cluster that a cell other
// than controlCellID observed within the window: edges still attached to a
// previous control cell. Nodes nobody observed within the window are offline
// and do not hold a reassignment open.
func (q *Queries) ListControlCellPendingNodes(ctx context.Context, clusterID, controlCellID string, window time.Duration) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT node_id
		FROM quartermaster.infrastructure_nodes
		WHERE cluster_id = $1
		  AND control_cell_observed_at > NOW() - ($3::double precision * INTERVAL '1 second')
		  AND control_cell_id IS DISTINCT FROM $2
		ORDER BY node_id
	`, clusterID, controlCellID, window.Seconds())
	return scanStrings(rows, err)
}

// CompleteClusterControlCellReassignment closes the switching reassignment
// opened at startedAt. previous_control_cell_id is kept so that cell keeps
// refusing edges that reconnect to it from a stale address.
func (q *Queries) CompleteClusterControlCellReassignment(ctx context.Context, clusterID string, startedAt time.Time) (bool, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_clusters
		SET reassignment_state = NULL,
		    reassignment_started_at = NULL,
		    reassignment_deadline_at = NULL,
		    reassignment_error = NULL,
		    updated_at = NOW()
		WHERE cluster_id = $1 AND reassignment_state = 'switching' AND reassignment_started_at = $2
	`, clusterID, startedAt)
	return rowsChanged(result, err)
}

// FailClusterControlCellReassignment marks the switching reassignment opened
// at startedAt failed. Control stays with the target cell and the previous
// cell keeps releasing edges; reassigning back is the rollback.
func (q *Queries) FailClusterControlCellReassignment(ctx context.Context, clusterID string, startedAt time.Time, reason string) (bool, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_clusters
		SET reassignment_state = 'failed',
		    reassignment_error = $3,
		    updated_at = NOW()
		WHERE cluster_id = $1 AND reassignment_state = 'switching' AND reassignment_started_at = $2
	`, clusterID, startedAt, reason)
	return rowsChanged(result, err)
}

func rowsChanged(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// ListReleasedControlCellClusters returns the tenant-private clusters whose
// previous control cell is served by the Foghorn instance and that the
// instance does not serve. That Foghorn releases and refuses their edges.
func (q *Queries) ListReleasedControlCellClusters(ctx context.Context, instanceID string) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT DISTINCT owned.cluster_id
		FROM quartermaster.service_instances si
		JOIN quartermaster.services svc
		  ON svc.service_id = si.service_id AND svc.type = 'foghorn'
		JOIN quartermaster.service_cluster_assignments cell_sca
		  ON cell_sca.service_instance_id = si.id AND cell_sca.is_active = true
		JOIN quartermaster.infrastructure_clusters owned
		  ON owned.previous_control_cell_id = cell_sca.cluster_id
		 AND owned.cluster_class = 'tenant_private'
		WHERE si.instance_id = $1
		  AND owned.control_cell_id IS DISTINCT FROM owned.previous_control_cell_id
		  AND NOT EXISTS (
		        SELECT 1
		        FROM quartermaster.service_cluster_assignments served
		        WHERE served.service_instance_id = si.id
		          AND served.cluster_id = owned.cluster_id
		          AND served.is_active = true
		  )
		ORDER BY owned.cluster_id
	`, instanceID)
	return scanStrings(rows, err)
}

// RecordNodeControlCellObservations stores cellID as the control cell of each
// node whose observation is at least as recent as the stored one, so a
// previous cell reporting an older snapshot cannot take a moved node back.
func (q *Queries) RecordNodeControlCellObservations(ctx context.Context, cellID string, nodeIDs []string, observedAt []time.Time) error {
	stamps := make([]string, len(observedAt))
	for i, observed := range observedAt {
		stamps[i] = observed.UTC().Format(time.RFC3339Nano)
	}
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes n
		SET control_cell_id = $1,
		    control_cell_observed_at = LEAST(payload.observed_at, NOW())
		FROM unnest($2::text[], $3::timestamptz[]) AS payload(node_id, observed_at)
		WHERE n.node_id = payload.node_id
		  AND (n.control_cell_observed_at IS NULL OR n.control_cell_observed_at <= LEAST(payload.observed_at, NOW()))
	`, cellID, pq.Array(nodeIDs), pq.Array(stamps))
	return err
}

// CountClusterControlCellReassignmentsByState counts active tenant-private
// clusters with an open or failed reassignment, keyed by state.
func (q *Queries) CountClusterControlCellReassignmentsByState(ctx context.Context) (map[string]int64, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT reassignment_state, COUNT(*)
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_class = 'tenant_private' AND is_active = true AND reassignment_state IS NOT NULL
		GROUP BY reassignment_state
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int64{}
	for rows.Next() {
		var state string
		var count int64
		if scanErr := rows.Scan(&state, &count); scanErr != nil {
			return nil, scanErr
		}
		counts[state] = count
	}
	return counts, rows.Err()
}
