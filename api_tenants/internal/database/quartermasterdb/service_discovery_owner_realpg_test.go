//go:build schema_verify

package quartermasterdb

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func assertGatewayDiscoverySeparatesAssignmentFromAuthOwner(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO quartermaster.services (service_id, name, plane, type, protocol) VALUES ('rc-gateway', 'RC gateway', 'control', 'livepeer-gateway', 'https')`,
		`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, control_cell_id) VALUES ('rc-assignment', 'Assigned tenant', 'edge', 'https://assigned.example.test', 'opaque-owner'), ('rc-physical', 'Physical gateway', 'edge', 'https://physical.example.test', 'opaque-physical-cell')`,
		`INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, status, health_status, last_health_check, protocol, port, metadata) VALUES ('rc-gateway-owner', 'rc-physical', 'rc-gateway', 'running', 'healthy', NOW(), 'https', 443, '{"physical_cluster_id":"forged","auth_control_cell_id":"forged","wallet_address":"fixture-wallet"}')`,
		`INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, is_active) SELECT id, 'rc-assignment', true FROM quartermaster.service_instances WHERE instance_id='rc-gateway-owner'`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := New(db).DiscoverServicesPage(t.Context(), ServiceDiscoveryFilter{
		ServiceType: "livepeer-gateway", ClusterID: "rc-assignment", Scope: DiscoveryScopeService,
		Pool: true, Physical: true, StaleThreshold: 300, Limit: 10,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("discover assigned gateway: rows=%d err=%v", len(rows), err)
	}
	if rows[0].ClusterID != "rc-assignment" {
		t.Fatal("discovery changed the pool assignment contract")
	}
	var metadata map[string]string
	if err := json.Unmarshal(rows[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["physical_cluster_id"] != "rc-physical" || metadata["auth_control_cell_id"] != "opaque-physical-cell" {
		t.Fatalf("pool scope or registrant metadata replaced physical authorization ownership: %v", metadata)
	}
	if metadata["wallet_address"] != "fixture-wallet" {
		t.Fatal("canonical ownership enrichment discarded unrelated metadata")
	}
}
