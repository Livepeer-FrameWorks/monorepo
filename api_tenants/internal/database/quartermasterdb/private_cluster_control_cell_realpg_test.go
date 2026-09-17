//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

func TestPrivateClusterControlCellFoghorns_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	q := New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	instanceUUID := func(instanceID string) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, `SELECT id::text FROM quartermaster.service_instances WHERE instance_id = $1`, instanceID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
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
	servedBy := func(instanceID string) []string {
		t.Helper()
		got, err := q.ListRunningServiceClusterAssignments(ctx, ListRunningServiceClusterAssignmentsParams{
			InstanceID: instanceID, ServiceType: sql.NullString{String: "foghorn", Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	exec(`
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, cell_id, control_cell_id)
		VALUES ('cell-eu', 'EU cell', 'edge', 'https://eu.example', 'platform_official', 'cell-eu', 'cell-eu'),
		       ('cell-us', 'US cell', 'edge', 'https://us.example', 'platform_official', 'cell-us', 'cell-us'),
		       ('private-eu', 'Private EU', 'edge', 'https://eu.example', 'tenant_private', 'private-eu', 'cell-eu'),
		       ('private-us', 'Private US', 'edge', 'https://us.example', 'tenant_private', 'private-us', 'cell-us');
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('cell-foghorn', 'Cell Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('foghorn-eu-1', 'cell-eu', 'cell-foghorn', 'grpc', 'eu-1.internal', 18019, 'running', 'healthy'),
		       ('foghorn-eu-2', 'cell-eu', 'cell-foghorn', 'grpc', 'eu-2.internal', 18019, 'running', 'healthy'),
		       ('foghorn-eu-3', 'cell-eu', 'cell-foghorn', 'grpc', 'eu-3.internal', 18019, 'running', 'healthy'),
		       ('foghorn-us-1', 'cell-us', 'cell-foghorn', 'grpc', 'us-1.internal', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, si.cluster_id, 'gitops_seed' FROM quartermaster.service_instances si;
	`)

	for clusterID, want := range map[string]int64{"private-eu": 3, "private-us": 1} {
		assigned, err := q.AssignControlCellFoghornsToPrivateCluster(ctx, clusterID)
		if err != nil || assigned != want {
			t.Fatalf("assign %s: %d instances, err %v; want %d", clusterID, assigned, err, want)
		}
	}
	if got := servedBy("foghorn-eu-2"); !slices.Contains(got, "private-eu") || slices.Contains(got, "private-us") {
		t.Fatalf("foghorn-eu-2 serves %v, want private-eu and not private-us", got)
	}

	exec(`UPDATE quartermaster.service_instances SET status = 'stopped', health_status = 'unhealthy' WHERE instance_id = 'foghorn-eu-1'`)
	addr, err := q.GetClusterInternalFoghornGRPC(ctx, "private-eu")
	if err != nil || !slices.Contains([]string{"eu-2.internal:18019", "eu-3.internal:18019"}, addr) {
		t.Fatalf("private-eu ConfigSeed dial after losing foghorn-eu-1 = %q, %v; want a surviving cell replica", addr, err)
	}

	drained, _, err := q.DrainServicePoolInstance(ctx, ServicePoolInstanceParams{InstanceID: instanceUUID("foghorn-eu-2"), ServiceType: "foghorn"})
	if err != nil || !slices.Equal(drained, []string{"cell-eu"}) {
		t.Fatalf("drain foghorn-eu-2 removed %v, %v; want only its platform cell assignment", drained, err)
	}
	if got := servedBy("foghorn-eu-2"); !slices.Equal(got, []string{"private-eu"}) {
		t.Fatalf("drained foghorn-eu-2 serves %v, want its private-eu assignment kept", got)
	}

	exec(`
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('foghorn-eu-4', 'cell-eu', 'cell-foghorn', 'grpc', 'eu-4.internal', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT id, 'cell-eu', 'gitops_seed' FROM quartermaster.service_instances WHERE instance_id = 'foghorn-eu-4';
		DELETE FROM quartermaster.service_cluster_assignments
		WHERE cluster_id = 'cell-eu'
		  AND service_instance_id = (SELECT id FROM quartermaster.service_instances WHERE instance_id = 'foghorn-eu-3');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT id, 'cell-us', 'gitops_seed' FROM quartermaster.service_instances WHERE instance_id = 'foghorn-eu-3';
	`)

	changed, err := q.ReconcilePrivateClusterControlCellFoghorns(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(changed)
	if !slices.Equal(changed, []string{"private-eu", "private-us"}) {
		t.Fatalf("reconcile changed %v, want private-eu and private-us", changed)
	}
	// foghorn-eu-1 stopped and foghorn-eu-3 moved to cell-us, so both leave
	// private-eu; foghorn-eu-2 is running without a platform assignment, as
	// mid-provision, and keeps it; foghorn-eu-4 joined cell-eu.
	if got := assignedInstances("private-eu"); !slices.Equal(got, []string{"foghorn-eu-2", "foghorn-eu-4"}) {
		t.Fatalf("private-eu Foghorns after reconcile = %v", got)
	}
	if got := assignedInstances("private-us"); !slices.Equal(got, []string{"foghorn-eu-3", "foghorn-us-1"}) {
		t.Fatalf("private-us Foghorns after reconcile = %v", got)
	}

	changed, err = q.ReconcilePrivateClusterControlCellFoghorns(ctx, "")
	if err != nil || len(changed) != 0 {
		t.Fatalf("second reconcile changed %v, %v; want no-op", changed, err)
	}
}
