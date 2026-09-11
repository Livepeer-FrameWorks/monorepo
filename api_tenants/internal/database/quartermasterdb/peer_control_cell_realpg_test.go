//go:build schema_verify

package quartermasterdb

import (
	"context"
	"testing"
)

func TestPeerDiscoveryCanonicalControlCells_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	const tenantID = "11111111-1111-4111-8111-111111111199"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants (id, name, deployment_tier)
		VALUES ($1::uuid, 'Peer cell contract', 'production');
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters
		(cluster_id, cluster_name, cluster_type, base_url, control_cell_id, cell_id)
		VALUES ('peer-source', 'Source', 'edge', 'https://source.example', 'source-cell', 'source-failure-cell'),
		('peer-explicit', 'Explicit', 'edge', 'https://explicit.example', 'explicit-control', 'different-failure-cell'),
		('peer-fallback', 'Fallback', 'edge', 'https://fallback.example', '', 'fallback-cell'),
		('peer-default', 'Default', 'edge', 'https://default.example', '', '');
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('peer-foghorn', 'Peer Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('peer-pool-instance', 'peer-source', 'peer-foghorn', 'grpc', 'foghorn.example', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
		SELECT si.id, ic.cluster_id FROM quartermaster.service_instances si
		CROSS JOIN quartermaster.infrastructure_clusters ic
		WHERE si.instance_id = 'peer-pool-instance' AND ic.cluster_id IN ('peer-explicit', 'peer-fallback', 'peer-default');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_source, subscription_status, is_active)
		SELECT $1::uuid, cluster_id, 'platform_tier', 'active', true FROM quartermaster.infrastructure_clusters
		WHERE cluster_id IN ('peer-source', 'peer-explicit', 'peer-fallback', 'peer-default')
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	rows, err := New(db).ListPeerClusters(ctx, "peer-source")
	if err != nil || len(rows) != 3 {
		t.Fatalf("peer discovery: %+v, %v", rows, err)
	}
	want := map[string]string{"peer-explicit": "explicit-control", "peer-fallback": "fallback-cell", "peer-default": "peer-default"}
	for _, row := range rows {
		if row.ControlCellID != want[row.ClusterID] || row.FoghornAddr != "foghorn.example:18019" || len(row.SharedTenantIds) != 1 || row.SharedTenantIds[0] != tenantID {
			t.Fatalf("peer assignment/identity/scope mismatch: %+v", row)
		}
	}
	if _, err = db.ExecContext(ctx, `UPDATE quartermaster.tenant_cluster_access SET is_active = false WHERE tenant_id = $1::uuid AND cluster_id = 'peer-explicit'`, tenantID); err != nil {
		t.Fatal(err)
	}
	rows, err = New(db).ListPeerClusters(ctx, "peer-source")
	if err != nil || len(rows) != 2 {
		t.Fatalf("revoked peer retained: %+v, %v", rows, err)
	}
	for _, row := range rows {
		if row.ClusterID == "peer-explicit" {
			t.Fatal("revoked peer retained cell address")
		}
	}
}
