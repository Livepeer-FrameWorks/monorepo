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

// A peer leader re-dials whenever a cluster's Foghorn address changes, so the
// address must not follow heartbeats: bumping either replica's updated_at in
// turn must leave the chosen address unchanged.
func TestPeerDiscoveryAddressIsStableAcrossReplicaHeartbeats_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	const tenantID = "11111111-1111-4111-8111-111111111198"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants (id, name, deployment_tier)
		VALUES ($1::uuid, 'Peer address stability', 'production');
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
		VALUES ('stable-source', 'Source', 'edge', 'https://source.example'),
		('stable-peer', 'Peer', 'edge', 'https://peer.example');
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('stable-foghorn', 'Stable Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status)
		VALUES ('stable-peer-b', 'stable-peer', 'stable-foghorn', 'grpc', 'foghorn-b', 18019, 'running', 'healthy'),
		('stable-peer-b-2', 'stable-peer', 'stable-foghorn', 'grpc', 'foghorn-b-2', 18019, 'running', 'healthy');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id)
		SELECT si.id, 'stable-peer' FROM quartermaster.service_instances si
		WHERE si.instance_id IN ('stable-peer-b', 'stable-peer-b-2');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_source, subscription_status, is_active)
		SELECT $1::uuid, cluster_id, 'platform_tier', 'active', true FROM quartermaster.infrastructure_clusters
		WHERE cluster_id IN ('stable-source', 'stable-peer')
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	peerAddr := func() string {
		t.Helper()
		rows, err := New(db).ListPeerClusters(ctx, "stable-source")
		if err != nil || len(rows) != 1 {
			t.Fatalf("peer discovery: %+v, %v", rows, err)
		}
		return rows[0].FoghornAddr
	}
	first := peerAddr()
	if first == "" {
		t.Fatal("no peer address for a cluster with two healthy replicas")
	}
	for _, heartbeat := range []string{"stable-peer-b", "stable-peer-b-2", "stable-peer-b", "stable-peer-b-2"} {
		if _, err := db.ExecContext(ctx, `UPDATE quartermaster.service_instances SET updated_at = NOW() + interval '1 second' WHERE instance_id = $1`, heartbeat); err != nil {
			t.Fatal(err)
		}
		if got := peerAddr(); got != first {
			t.Fatalf("peer address moved from %q to %q after %s heartbeated", first, got, heartbeat)
		}
	}
}
