//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestClusterControlCellReassignment_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	q := New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	assignedInstances := func(clusterID string) []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `
			SELECT si.instance_id
			FROM quartermaster.service_cluster_assignments sca
			JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
			WHERE sca.cluster_id = $1 AND sca.is_active = true
			ORDER BY si.instance_id`, clusterID)
		got, err := scanStrings(rows, err)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	observe := func(cellID string, nodeIDs []string, observedAt ...time.Time) {
		t.Helper()
		if err := q.RecordNodeControlCellObservations(ctx, cellID, nodeIDs, observedAt); err != nil {
			t.Fatal(err)
		}
	}
	pending := func() []string {
		t.Helper()
		got, err := q.ListControlCellPendingNodes(ctx, "private-eu", "cell-us", 3*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	released := func(instanceID string) []string {
		t.Helper()
		got, err := q.ListReleasedControlCellClusters(ctx, instanceID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	exec(`
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, cell_id, control_cell_id, eligible_serving_cell_ids)
		VALUES ('cell-eu', 'EU cell', 'edge', 'https://eu.example', 'platform_official', 'cell-eu', 'cell-eu', ARRAY[]::TEXT[]),
		       ('cell-us', 'US cell', 'edge', 'https://us.example', 'platform_official', 'cell-us', 'cell-us', ARRAY[]::TEXT[]),
		       ('private-eu', 'Private EU', 'edge', 'https://eu.example', 'tenant_private', 'private-eu', 'cell-eu', ARRAY['cell-eu']);
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('cell-foghorn', 'Cell Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('foghorn-eu-1', 'cell-eu', 'cell-foghorn', 'grpc', 'eu-1.internal', 18019, 'running', 'healthy'),
		       ('foghorn-us-1', 'cell-us', 'cell-foghorn', 'grpc', 'us-1.internal', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, si.cluster_id, 'gitops_seed' FROM quartermaster.service_instances si;
		INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type)
		VALUES ('edge-1', 'private-eu', 'edge-1', 'edge'),
		       ('edge-2', 'private-eu', 'edge-2', 'edge'),
		       ('edge-3', 'private-eu', 'edge-3', 'edge');
	`)
	if assigned, err := q.AssignControlCellFoghornsToPrivateCluster(ctx, "private-eu"); err != nil || assigned != 1 {
		t.Fatalf("assign private-eu to cell-eu: %d, %v", assigned, err)
	}
	for cellID, want := range map[string]bool{"cell-us": true, "private-eu": false, "missing": false} {
		if got, err := q.ControlCellHasRunningFoghorn(ctx, cellID); err != nil || got != want {
			t.Fatalf("ControlCellHasRunningFoghorn(%s) = %v, %v; want %v", cellID, got, err, want)
		}
	}

	now := time.Now()
	observe("cell-eu", []string{"edge-1", "edge-2", "edge-3"}, now.Add(-10*time.Second), now.Add(-10*time.Second), now.Add(-time.Hour))

	started, err := q.StartClusterControlCellReassignment(ctx, StartClusterControlCellReassignmentParams{
		ClusterID: "private-eu", TargetCellID: "cell-us", DeadlineAt: now.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.State != "switching" || started.ControlCellID != "cell-us" || started.PreviousControlCellID != "cell-eu" || !started.StartedAt.Valid {
		t.Fatalf("started reassignment = %+v", started)
	}
	if _, err := q.StartClusterControlCellReassignment(ctx, StartClusterControlCellReassignmentParams{
		ClusterID: "private-eu", TargetCellID: "cell-eu", DeadlineAt: now.Add(time.Hour),
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second start while switching = %v, want sql.ErrNoRows", err)
	}
	var eligible []string
	rows, err := db.QueryContext(ctx, `SELECT unnest(eligible_serving_cell_ids) FROM quartermaster.infrastructure_clusters WHERE cluster_id = 'private-eu'`)
	if eligible, err = scanStrings(rows, err); err != nil || !slices.Equal(eligible, []string{"cell-us"}) {
		t.Fatalf("eligible serving cells after start = %v, %v", eligible, err)
	}

	if assigned, err := q.AssignControlCellFoghornsToPrivateCluster(ctx, "private-eu"); err != nil || assigned != 1 {
		t.Fatalf("assign private-eu to cell-us: %d, %v", assigned, err)
	}
	if _, err := q.ReconcilePrivateClusterControlCellFoghorns(ctx, "private-eu"); err != nil {
		t.Fatal(err)
	}
	if got := assignedInstances("private-eu"); !slices.Equal(got, []string{"foghorn-us-1"}) {
		t.Fatalf("private-eu Foghorns after the switch = %v", got)
	}
	if got := released("foghorn-eu-1"); !slices.Equal(got, []string{"private-eu"}) {
		t.Fatalf("clusters released by foghorn-eu-1 = %v", got)
	}
	if got := released("foghorn-us-1"); len(got) != 0 {
		t.Fatalf("clusters released by foghorn-us-1 = %v", got)
	}

	// edge-3 was last seen an hour ago: offline edges do not hold the move open.
	if got := pending(); !slices.Equal(got, []string{"edge-1", "edge-2"}) {
		t.Fatalf("pending nodes after start = %v", got)
	}
	observe("cell-us", []string{"edge-1"}, now.Add(-5*time.Second))
	observe("cell-eu", []string{"edge-1"}, now.Add(-8*time.Second))
	if got := pending(); !slices.Equal(got, []string{"edge-2"}) {
		t.Fatalf("pending nodes after edge-1 moved and cell-eu replayed an older snapshot = %v", got)
	}

	stale := started.StartedAt.Time.Add(-time.Second)
	if done, err := q.CompleteClusterControlCellReassignment(ctx, "private-eu", stale); err != nil || done {
		t.Fatalf("complete with a stale start fence = %v, %v", done, err)
	}
	if failed, err := q.FailClusterControlCellReassignment(ctx, "private-eu", stale, "stale"); err != nil || failed {
		t.Fatalf("fail with a stale start fence = %v, %v", failed, err)
	}

	observe("cell-us", []string{"edge-2"}, now.Add(-2*time.Second))
	if got := pending(); len(got) != 0 {
		t.Fatalf("pending nodes after every live edge moved = %v", got)
	}
	if done, err := q.CompleteClusterControlCellReassignment(ctx, "private-eu", started.StartedAt.Time); err != nil || !done {
		t.Fatalf("complete reassignment = %v, %v", done, err)
	}
	switching, err := q.ListSwitchingClusterControlCellReassignments(ctx)
	if err != nil || len(switching) != 0 {
		t.Fatalf("switching reassignments after completion = %v, %v", switching, err)
	}
	record, err := q.GetClusterControlCellReassignment(ctx, "private-eu")
	if err != nil || record.State != "" || record.PreviousControlCellID != "cell-eu" || record.StartedAt.Valid {
		t.Fatalf("record after completion = %+v, %v", record, err)
	}
	if got := released("foghorn-eu-1"); !slices.Equal(got, []string{"private-eu"}) {
		t.Fatalf("previous cell stops refusing edges after completion: released %v", got)
	}

	back, err := q.StartClusterControlCellReassignment(ctx, StartClusterControlCellReassignmentParams{
		ClusterID: "private-eu", TargetCellID: "cell-eu", DeadlineAt: time.Now().Add(time.Minute),
	})
	if err != nil || back.PreviousControlCellID != "cell-us" {
		t.Fatalf("reassign back = %+v, %v", back, err)
	}
	if failed, err := q.FailClusterControlCellReassignment(ctx, "private-eu", back.StartedAt.Time, "edges did not move"); err != nil || !failed {
		t.Fatalf("fail reassignment = %v, %v", failed, err)
	}
	record, err = q.GetClusterControlCellReassignment(ctx, "private-eu")
	if err != nil || record.State != "failed" || record.Error != "edges did not move" || record.ControlCellID != "cell-eu" {
		t.Fatalf("record after failure = %+v, %v", record, err)
	}
	if counts, err := q.CountClusterControlCellReassignmentsByState(ctx); err != nil || counts["failed"] != 1 || counts["switching"] != 0 {
		t.Fatalf("reassignment counts after failure = %v, %v; want one failed", counts, err)
	}
	if _, err := q.StartClusterControlCellReassignment(ctx, StartClusterControlCellReassignmentParams{
		ClusterID: "private-eu", TargetCellID: "cell-us", DeadlineAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("reassign from a failed reassignment: %v", err)
	}
}
