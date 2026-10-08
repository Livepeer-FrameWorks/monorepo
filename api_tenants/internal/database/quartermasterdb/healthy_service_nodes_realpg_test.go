//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestListHealthyServiceNodesConsistentRead_RealPG(t *testing.T) {
	testListHealthyServiceNodesConsistentRead(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestListHealthyServiceNodesConsistentRead_RealYugabyte(t *testing.T) {
	testListHealthyServiceNodesConsistentRead(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

// writeAfterFirstRead is a DBTX that commits a concurrent write on its own
// connection right after the reader's first statement, the window a
// multi-statement read leaves open to Quartermaster's health and lifecycle
// writers.
type writeAfterFirstRead struct {
	*sql.DB
	once  sync.Once
	write func()
}

func (w *writeAfterFirstRead) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := w.DB.QueryRowContext(ctx, query, args...)
	w.once.Do(w.write)
	return row
}

func (w *writeAfterFirstRead) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := w.DB.QueryContext(ctx, query, args...)
	w.once.Do(w.write)
	return rows, err
}

// Production 2026-10-08: concurrent DNS wakes read counts and rows from three
// statements and saw one candidate with no healthy node while the node was
// healthy, which Navigator then published as an empty record set. Every
// answer must be the state before or after a concurrent write, never a mix.
func testListHealthyServiceNodesConsistentRead(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('edge-egress', 'Edge egress', 'control', 'edge-egress', 'http')
		ON CONFLICT (service_id) DO NOTHING`)

	seedHealthyEdge := func(clusterID, nodeID, ip string) {
		t.Helper()
		exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
			VALUES ($1, $2, 'edge', $3)
			ON CONFLICT (cluster_id) DO NOTHING`, clusterID, clusterID, "https://"+clusterID+".example.com")
		exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, external_ip, status)
			VALUES ($1, $2, $3, 'edge', $4::inet, 'active')`, nodeID, clusterID, nodeID, ip)
		exec(`INSERT INTO quartermaster.service_instances
				(instance_id, cluster_id, node_id, service_id, advertise_host, port, status, health_status, last_health_check)
			VALUES ($1, $2, $3, 'edge-egress', $4, 18008, 'running', 'healthy', NOW())`, nodeID+"-edge-egress", clusterID, nodeID, ip)
	}

	type answer struct{ total, healthy, rows int32 }
	read := func(clusterID string, write func()) answer {
		t.Helper()
		reader := &writeAfterFirstRead{DB: db, write: write}
		rows, total, healthy, err := New(reader).ListHealthyServiceNodes(ctx, HealthyNodeFilter{
			Scope: NodeScopeService, ClusterID: clusterID, ServiceType: "edge-egress", StaleThreshold: 300,
		})
		if err != nil {
			t.Fatalf("ListHealthyServiceNodes(%s): %v", clusterID, err)
		}
		return answer{total: total, healthy: healthy, rows: int32(len(rows))}
	}
	expectOneOf := func(name string, got answer, allowed ...answer) {
		t.Helper()
		for _, want := range allowed {
			if got == want {
				return
			}
		}
		t.Fatalf("%s: (total, healthy, rows) = %+v, want one consistent snapshot of %+v", name, got, allowed)
	}

	// The only edge is taken out of service while the read runs.
	seedHealthyEdge("dns-read-drain", "dns-read-drain-1", "198.51.100.20")
	got := read("dns-read-drain", func() {
		exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'offline' WHERE node_id = 'dns-read-drain-1'`)
	})
	expectOneOf("node drained during read", got, answer{1, 1, 1}, answer{0, 0, 0})

	// A second healthy edge joins while the read runs.
	seedHealthyEdge("dns-read-join", "dns-read-join-1", "198.51.100.30")
	got = read("dns-read-join", func() {
		seedHealthyEdge("dns-read-join", "dns-read-join-2", "198.51.100.31")
	})
	expectOneOf("node joined during read", got, answer{1, 1, 1}, answer{2, 2, 2})

	// Unhealthy candidates count toward the total but are not returned.
	seedHealthyEdge("dns-read-mixed", "dns-read-mixed-1", "198.51.100.40")
	seedHealthyEdge("dns-read-mixed", "dns-read-mixed-2", "198.51.100.41")
	exec(`UPDATE quartermaster.service_instances SET health_status = 'unhealthy' WHERE node_id = 'dns-read-mixed-2'`)
	got = read("dns-read-mixed", func() {})
	expectOneOf("one of two unhealthy", got, answer{2, 1, 1})

	// A stale health check is not healthy.
	seedHealthyEdge("dns-read-stale", "dns-read-stale-1", "198.51.100.50")
	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() - INTERVAL '1 hour' WHERE node_id = 'dns-read-stale-1'`)
	got = read("dns-read-stale", func() {})
	expectOneOf(fmt.Sprintf("stale health check (threshold %ds)", 300), got, answer{1, 0, 0})
}
