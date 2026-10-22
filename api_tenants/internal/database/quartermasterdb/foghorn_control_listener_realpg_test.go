//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Foghorn marks the registration of its internal control listener; routing
// follows the marker, not the port number the listener happens to use.
func TestFoghornControlListenerSelectedByMarker_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	q := New(db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, cell_id, control_cell_id)
		VALUES ('marked-cell', 'Marked', 'edge', 'https://marked.example', 'platform_official', 'marked-cell', 'marked-cell'),
		       ('unmarked-cell', 'Unmarked', 'edge', 'https://unmarked.example', 'platform_official', 'unmarked-cell', 'unmarked-cell');
		INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('marker-foghorn', 'Marker Foghorn', 'control', 'foghorn', 'grpc');
		INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, protocol, advertise_host, port, status, health_status, metadata)
		VALUES ('marked', 'marked-cell', 'marker-foghorn', 'grpc', 'marked.internal', 18119, 'running', 'healthy',
		        '{"foghorn_listener":"internal_control","external_grpc_port":"18129"}'),
		       ('unmarked', 'unmarked-cell', 'marker-foghorn', 'grpc', 'unmarked.internal', 18019, 'running', 'healthy', '{}');
		INSERT INTO quartermaster.service_cluster_assignments (service_instance_id, cluster_id, source)
		SELECT si.id, si.cluster_id, 'gitops_seed' FROM quartermaster.service_instances si
		WHERE si.instance_id IN ('marked', 'unmarked');
	`); err != nil {
		t.Fatal(err)
	}

	if addr, err := q.GetClusterInternalFoghornGRPC(ctx, "marked-cell"); err != nil || addr != "marked.internal:18119" {
		t.Fatalf("marked cell control listener = %q, %v; want marked.internal:18119 on its own port", addr, err)
	}
	if addr, err := q.GetClusterInternalFoghornGRPC(ctx, "unmarked-cell"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unmarked cell control listener = %q, %v; want none for a gRPC row on 18019 without the marker", addr, err)
	}
	if port, err := q.GetClusterFoghornExternalGRPCPort(ctx, "marked-cell"); err != nil || port.String != "18129" {
		t.Fatalf("marked cell external port = %+v, %v; want the registered 18129", port, err)
	}
}
