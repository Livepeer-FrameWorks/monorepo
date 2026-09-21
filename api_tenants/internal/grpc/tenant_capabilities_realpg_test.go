//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The capability cluster set is the GetClusterRouting peer set across every
// grant state, and a media capability needs consent plus a fresh, healthy
// report from an active edge node.
func TestTenantClusterCapabilities_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-tenant-capabilities-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	verifyTenantClusterCapabilities(t, db)
}

func TestTenantClusterCapabilities_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "quartermaster_capabilities")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyTenantClusterCapabilities(t, db)
}

func verifyTenantClusterCapabilities(t *testing.T, db *sql.DB) { //nolint:funlen // One engine fixture proves entitlement parity and every freshness flip.
	t.Helper()
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	const (
		tenant = "11111111-1111-4111-8111-111111111181"
		other  = "11111111-1111-4111-8111-111111111182"
	)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, cluster := range []string{
		"cap-preferred", "cap-official", "cap-subscribed", "cap-pending", "cap-expired",
		"cap-deactivated", "cap-unknown-source", "cap-cluster-inactive", "cap-other-tenant",
	} {
		exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
		      VALUES ($1::varchar, $1::varchar, 'edge', 'https://' || $1::varchar || '.example')`, cluster)
	}
	exec(`UPDATE quartermaster.infrastructure_clusters SET is_active = false WHERE cluster_id = 'cap-cluster-inactive'`)
	exec(`UPDATE quartermaster.infrastructure_clusters SET media_allow_ingest = false WHERE cluster_id = 'cap-official'`)
	exec(`INSERT INTO quartermaster.tenants (id, name, primary_cluster_id, official_cluster_id, custom_subdomain_enabled)
	      VALUES ($1::uuid, 'Capabilities', 'cap-preferred', 'cap-official', true), ($2::uuid, 'Other', 'cap-other-tenant', NULL, false)`, tenant, other)
	for _, grant := range []struct {
		tenant, cluster, source, status string
		active                          bool
		expires                         string
	}{
		{tenant, "cap-preferred", "platform_tier", "active", true, ""},
		{tenant, "cap-official", "platform_tier", "active", true, ""},
		{tenant, "cap-subscribed", "marketplace_subscription", "active", true, "future"},
		{tenant, "cap-pending", "marketplace_subscription", "pending_approval", true, ""},
		{tenant, "cap-expired", "private_invite", "active", true, "past"},
		{tenant, "cap-deactivated", "private_invite", "active", false, ""},
		{tenant, "cap-unknown-source", "unknown", "active", true, ""},
		{tenant, "cap-cluster-inactive", "platform_tier", "active", true, ""},
		{other, "cap-other-tenant", "owner", "active", true, ""},
	} {
		exec(`INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_source, subscription_status, is_active, expires_at)
		      VALUES ($1::uuid, $2, $3, $4, $5,
		              CASE $6 WHEN 'past' THEN NOW() - INTERVAL '1 hour' WHEN 'future' THEN NOW() + INTERVAL '1 day' ELSE NULL END)`,
			grant.tenant, grant.cluster, grant.source, grant.status, grant.active, grant.expires)
	}
	for _, serviceType := range []string{edgeIngestServiceType, edgeEgressServiceType, edgeStorageServiceType, edgeProcessingServiceType} {
		exec(`INSERT INTO quartermaster.services (service_id, name, plane, type) VALUES ($1, $1, 'edge', $1)`, serviceType)
	}
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status) VALUES
	      ('cap-edge-a', 'cap-preferred', 'Edge A', 'edge', 'active'),
	      ('cap-edge-offline', 'cap-preferred', 'Edge offline', 'edge', 'offline'),
	      ('cap-official-edge', 'cap-official', 'Official edge', 'edge', 'active')`)
	instance := func(instanceID, nodeID, clusterID, serviceType, health, age string) {
		t.Helper()
		exec(`INSERT INTO quartermaster.service_instances (instance_id, cluster_id, node_id, service_id, status, health_status, last_health_check)
		      VALUES ($1, $2, $3, $4, 'running', $5, NOW() - $6::interval)`, instanceID, clusterID, nodeID, serviceType, health, age)
	}
	instance("cap-a-ingest", "cap-edge-a", "cap-preferred", edgeIngestServiceType, "healthy", "0 seconds")
	instance("cap-a-egress", "cap-edge-a", "cap-preferred", edgeEgressServiceType, "healthy", "1 hour")
	instance("cap-a-storage", "cap-edge-a", "cap-preferred", edgeStorageServiceType, "unhealthy", "0 seconds")
	instance("cap-offline-processing", "cap-edge-offline", "cap-preferred", edgeProcessingServiceType, "healthy", "0 seconds")
	instance("cap-official-ingest", "cap-official-edge", "cap-official", edgeIngestServiceType, "healthy", "0 seconds")
	instance("cap-official-egress", "cap-official-edge", "cap-official", edgeEgressServiceType, "healthy", "0 seconds")

	server := &QuartermasterServer{db: db, logger: logging.NewLogger(), physicalEndpointStaleSeconds: 300}
	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	routing, err := server.GetClusterRouting(serviceCtx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenant})
	if err != nil {
		t.Fatalf("GetClusterRouting: %v", err)
	}
	read := func(ctx context.Context) *quartermasterpb.GetTenantClusterCapabilitiesResponse {
		t.Helper()
		got, readErr := server.GetTenantClusterCapabilities(ctx, &quartermasterpb.GetTenantClusterCapabilitiesRequest{TenantId: tenant})
		if readErr != nil {
			t.Fatalf("GetTenantClusterCapabilities: %v", readErr)
		}
		return got
	}
	capabilities := read(serviceCtx)

	routingRoles, capabilityRoles := map[string]string{}, map[string]string{}
	for _, peer := range routing.GetClusterPeers() {
		routingRoles[peer.GetClusterId()] = peer.GetRole()
	}
	for _, cluster := range capabilities.GetClusters() {
		capabilityRoles[cluster.GetClusterId()] = cluster.GetRole()
	}
	wantRoles := map[string]string{"cap-official": "official", "cap-preferred": "preferred", "cap-subscribed": "subscribed"}
	if fmt.Sprint(routingRoles) != fmt.Sprint(wantRoles) || fmt.Sprint(capabilityRoles) != fmt.Sprint(routingRoles) {
		t.Fatalf("routing peers %v, capability clusters %v, want both %v", routingRoles, capabilityRoles, wantRoles)
	}
	if !capabilities.GetCustomSubdomain() || capabilities.GetCustomDomain() || capabilities.GetObservedAt() == nil {
		t.Fatalf("tenant capabilities = %+v", capabilities)
	}

	assertMedia := func(response *quartermasterpb.GetTenantClusterCapabilitiesResponse, clusterID string, want *quartermasterpb.ClusterMediaCapabilities) {
		t.Helper()
		for _, cluster := range response.GetClusters() {
			if cluster.GetClusterId() != clusterID {
				continue
			}
			got := cluster.GetMedia()
			if got.GetIngest() != want.GetIngest() || got.GetPlayback() != want.GetPlayback() || got.GetStorage() != want.GetStorage() || got.GetProcessing() != want.GetProcessing() {
				t.Fatalf("%s media = %+v, want %+v", clusterID, got, want)
			}
			return
		}
		t.Fatalf("%s missing from %+v", clusterID, response.GetClusters())
	}
	// Fresh ingest counts; a stale egress, an unhealthy storage report, and a
	// processing report from an offline node do not.
	assertMedia(capabilities, "cap-preferred", &quartermasterpb.ClusterMediaCapabilities{Ingest: true})
	// Fresh ingest and egress reports, but the capacity owner withdrew ingest.
	assertMedia(capabilities, "cap-official", &quartermasterpb.ClusterMediaCapabilities{Playback: true})
	assertMedia(capabilities, "cap-subscribed", &quartermasterpb.ClusterMediaCapabilities{})

	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() WHERE instance_id = 'cap-a-egress'`)
	exec(`UPDATE quartermaster.service_instances SET health_status = 'healthy' WHERE instance_id = 'cap-a-storage'`)
	exec(`UPDATE quartermaster.infrastructure_nodes SET status = 'active' WHERE node_id = 'cap-edge-offline'`)
	assertMedia(read(serviceCtx), "cap-preferred", &quartermasterpb.ClusterMediaCapabilities{Ingest: true, Playback: true, Storage: true, Processing: true})

	exec(`UPDATE quartermaster.service_instances SET last_health_check = NOW() - INTERVAL '301 seconds' WHERE instance_id = 'cap-a-ingest'`)
	exec(`UPDATE quartermaster.service_instances SET health_status = 'unhealthy' WHERE instance_id = 'cap-official-egress'`)
	refreshed := read(serviceCtx)
	assertMedia(refreshed, "cap-preferred", &quartermasterpb.ClusterMediaCapabilities{Playback: true, Storage: true, Processing: true})
	assertMedia(refreshed, "cap-official", &quartermasterpb.ClusterMediaCapabilities{})

	// Revoking the preferred grant removes it from both sets and routing falls
	// back to the official cluster.
	exec(`UPDATE quartermaster.tenant_cluster_access SET is_active = false WHERE tenant_id = $1::uuid AND cluster_id = 'cap-preferred'`, tenant)
	revoked := read(serviceCtx)
	ids := []string{}
	for _, cluster := range revoked.GetClusters() {
		ids = append(ids, cluster.GetClusterId()+"="+cluster.GetRole())
	}
	if !slices.Equal(ids, []string{"cap-official=preferred", "cap-subscribed=subscribed"}) {
		t.Fatalf("clusters after revocation = %v", ids)
	}
	if !revoked.GetCustomSubdomain() {
		t.Fatalf("custom subdomain lost with an entitled cluster remaining: %+v", revoked)
	}

	member := context.WithValue(tenantCtx(tenant, "member"), ctxkeys.KeyUserID, "member-user")
	if got := read(member); len(got.GetClusters()) != len(revoked.GetClusters()) {
		t.Fatalf("member read = %+v", got)
	}
	outsider := context.WithValue(tenantCtx(other, "owner"), ctxkeys.KeyUserID, "outsider")
	if _, err := server.GetTenantClusterCapabilities(outsider, &quartermasterpb.GetTenantClusterCapabilitiesRequest{TenantId: tenant}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-tenant read error = %v, want PermissionDenied", err)
	}
}
