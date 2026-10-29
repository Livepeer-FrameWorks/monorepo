//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Exercise a cluster created by the tenant RPCs, rather than a seed that
// already declares the cluster healthy. This fixture runs on both engines.
func verifyOwnedPrivateClusterReadiness(t *testing.T, db *sql.DB, server *QuartermasterServer, tenantID string) {
	t.Helper()
	ctx := context.Background()
	var clusterID string
	if err := db.QueryRowContext(ctx, `SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE owner_tenant_id = $1::uuid`, tenantID).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}
	health := func(want string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, `SELECT health_status FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, clusterID, tenantID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("tenant-created private cluster health = %q, want %q", got, want)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status)
	      VALUES ('owned-ready-edge', $1, 'Ready edge', 'edge', 'active')`, clusterID)
	report := func(healthy bool) {
		t.Helper()
		if _, err := server.ReportAliveNodes(serviceCtx(), &quartermasterpb.ReportAliveNodesRequest{
			Nodes: []*quartermasterpb.NodeAliveness{{NodeId: "owned-ready-edge", ClusterId: clusterID, IsHealthy: healthy,
				Capabilities: &quartermasterpb.EdgeCapabilities{Ingest: true, Egress: true}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	health("unknown")
	report(false)
	health("unknown")
	exec(`UPDATE quartermaster.infrastructure_clusters SET is_active = false WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, clusterID, tenantID)
	report(true)
	health("unknown")
	exec(`UPDATE quartermaster.infrastructure_clusters SET is_active = true WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, clusterID, tenantID)
	exec(`UPDATE quartermaster.tenants SET is_active = false WHERE id = $1::uuid`, tenantID)
	report(true)
	health("unknown")
	exec(`UPDATE quartermaster.tenants SET is_active = true WHERE id = $1::uuid`, tenantID)
	exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'maintenance' WHERE node_id = 'owned-ready-edge'`)
	report(true)
	health("unknown")
	exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'active' WHERE node_id = 'owned-ready-edge'`)
	report(true)
	health("healthy")
	var activatedAt, repeatedAt string
	if err := db.QueryRowContext(ctx, `SELECT updated_at::text FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, clusterID, tenantID).Scan(&activatedAt); err != nil {
		t.Fatal(err)
	}
	report(true)
	if err := db.QueryRowContext(ctx, `SELECT updated_at::text FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1 AND owner_tenant_id = $2::uuid`, clusterID, tenantID).Scan(&repeatedAt); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM quartermaster.service_event_outbox WHERE tenant_id = $1::uuid AND resource_id = $2 AND event_type = 'cluster_updated'`, tenantID, clusterID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if activatedAt != repeatedAt || events != 1 {
		t.Fatalf("repeated readiness changed timestamp %q -> %q or duplicated owner events: %d", activatedAt, repeatedAt, events)
	}
	// The owning tenant must see the same readiness Commodore admits against.
	exec(`UPDATE quartermaster.tenants SET primary_cluster_id = $1 WHERE id = $2::uuid`, clusterID, tenantID)
	route, err := server.GetClusterRouting(serviceCtx(), &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, peer := range route.GetClusterPeers() {
		if peer.GetClusterId() == clusterID {
			found = true
			if peer.GetHealthStatus() != "healthy" {
				t.Fatalf("private cluster admission peer health = %q, want healthy", peer.GetHealthStatus())
			}
		}
	}
	if !found {
		t.Fatal("owner's admission route omits the private cluster")
	}
	for _, denied := range []string{"degraded", "offline", "unhealthy", "provisioning"} {
		exec(`UPDATE quartermaster.infrastructure_clusters SET health_status = $1 WHERE cluster_id = $2 AND owner_tenant_id = $3::uuid`, denied, clusterID, tenantID)
		report(true)
		health(denied)
	}
}
