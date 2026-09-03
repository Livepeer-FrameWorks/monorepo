//go:build schema_verify

package quartermasterdb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareQuartermasterQueryCatalog(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareQuartermasterQueryCatalog(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func prepareQuartermasterQueryCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := quartermasterGeneratedQueries(t)
	if len(queries) != 163 {
		t.Fatalf("found %d generated Quartermaster queries, want 163", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("quartermaster_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

func TestManualQueryAdapters_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := New(tx).DeferBootstrapNodeClusterConstraints(ctx); err != nil {
		t.Fatalf("defer node cluster constraints: %v", err)
	}
	const tenantID = "11111111-1111-1111-1111-111111111111"
	if _, err := tx.ExecContext(ctx, `INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Contract tenant')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	queries := New(tx)
	id, err := queries.EnqueueNavigatorTenantAlias(ctx, EnqueueNavigatorTenantAliasParams{
		TenantID: tenantID, Subdomain: "contract", Action: "ensure",
	})
	if err != nil {
		t.Fatalf("enqueue tenant alias: %v", err)
	}
	if err := queries.FailNavigatorTenantAliasOutbox(ctx, FailNavigatorTenantAliasOutboxParams{
		ID: id, LastError: "contract failure", RetryInterval: "250 milliseconds",
	}); err != nil {
		t.Fatalf("fail tenant alias: %v", err)
	}
	var attempts int
	var retryScheduled bool
	if err := tx.QueryRowContext(ctx, `
		SELECT attempts, next_retry_at IS NOT NULL
		FROM quartermaster.navigator_tenant_alias_outbox
		WHERE id = $1::uuid
	`, id).Scan(&attempts, &retryScheduled); err != nil {
		t.Fatalf("read failed tenant alias: %v", err)
	}
	if attempts != 1 || !retryScheduled {
		t.Fatalf("unexpected failed tenant alias: attempts=%d retry_scheduled=%v", attempts, retryScheduled)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters
			(cluster_id, cluster_name, cluster_type, base_url, cluster_class)
		VALUES ('contract-cluster', 'Contract cluster', 'edge', 'https://contract.example.com', 'platform_official');
		INSERT INTO quartermaster.infrastructure_nodes
			(node_id, cluster_id, node_name, node_type, wireguard_ip)
		VALUES ('contract-node', 'contract-cluster', 'Contract node', 'edge', '10.44.0.2');
		INSERT INTO quartermaster.services
			(service_id, name, plane, type, protocol)
		VALUES ('contract-service', 'Contract service', 'control', 'contract-service', 'http');
	`); err != nil {
		t.Fatalf("seed service bootstrap adapter state: %v", err)
	}
	location, err := queries.GetNodeBootstrapLocation(ctx, "contract-node")
	if err != nil || location.ClusterID != "contract-cluster" || !location.NodeIP.Valid || location.NodeIP.String != "10.44.0.2" {
		t.Fatalf("unexpected node bootstrap location: %+v err=%v", location, err)
	}
	if err := queries.CreateBootstrappedServiceInstance(ctx, CreateBootstrappedServiceInstanceParams{
		InstanceID: "contract-instance", ClusterID: "contract-cluster", ServiceID: "contract-service",
		Protocol: "http", AdvertiseHost: "10.44.0.2", Version: "v1", Port: 8080,
	}); err != nil {
		t.Fatalf("create bootstrapped service instance: %v", err)
	}
	metadata := `{"listener":"contract"}`
	if err := queries.UpdateBootstrappedServiceInstance(ctx, UpdateBootstrappedServiceInstanceParams{
		AdvertiseHost: "10.44.0.2", Version: "v2", Metadata: &metadata,
		Protocol: "http", Port: 8080,
		ID: mustServiceInstanceID(t, tx, "contract-instance"),
	}); err != nil {
		t.Fatalf("update bootstrapped service instance: %v", err)
	}
	cleanup := StopDuplicateServiceInstancesParams{
		ServiceID: "contract-service", ClusterID: "contract-cluster", InstanceID: "contract-instance",
		AdvertiseHost: "10.44.0.2", Protocol: "http", Port: 8080,
	}
	if err := queries.StopDuplicateServiceInstances(ctx, cleanup); err != nil {
		t.Fatalf("stop duplicate service instances: %v", err)
	}
	if err := queries.StopStaleFoghornControlListeners(ctx, StopStaleFoghornControlListenersParams(cleanup)); err != nil {
		t.Fatalf("stop stale control listeners: %v", err)
	}
}

func TestConvertedRuntimeAdapters_RealPG(t *testing.T) {
	runConvertedRuntimeAdapters(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestConvertedRuntimeAdapters_RealYugabyte(t *testing.T) {
	runConvertedRuntimeAdapters(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func runConvertedRuntimeAdapters(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	seed, err := dbsql.Content.ReadFile("seeds/demo/postgres/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(seed)); err != nil {
		t.Fatalf("apply Quartermaster fixture: %v", err)
	}

	queries := New(db)
	tenantID := "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	cursorID := "00000000-0000-0000-0000-000000000000"
	cursorTime := time.Now().Add(time.Hour)
	truth := true
	kind := "edge_node"

	checks := []struct {
		name string
		run  func() error
	}{
		{"clusters/default", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopeDefault, Limit: 20})
			return err
		}},
		{"clusters/owner-filtered", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopeOwner, ScopeID: tenantID, ClusterID: "demo-media", ClusterName: "Demo", ClusterType: "edge", DeploymentModel: "shared", IsPlatformOfficial: &truth, PublicTopology: &truth, CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"clusters/platform", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopePlatform, Limit: 20})
			return err
		}},
		{"clusters/topology", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopePublicTopology, Limit: 20})
			return err
		}},
		{"clusters/tenant", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopeTenant, ScopeID: tenantID, Limit: 20})
			return err
		}},
		{"clusters/service", func() error {
			_, _, err := queries.ListClustersPage(ctx, ClusterListFilter{Scope: ClusterScopeService, Limit: 20})
			return err
		}},
		{"tenant-access", func() error {
			_, _, err := queries.ListTenantClusterAccessPage(ctx, SimplePageFilter{ScopeID: tenantID, Limit: 20})
			return err
		}},
		{"available-clusters/cursor", func() error {
			_, _, err := queries.ListAvailableClustersPage(ctx, SimplePageFilter{CursorTime: &cursorTime, CursorID: "demo-media", Backward: true, Limit: 20})
			return err
		}},
		{"subscriptions", func() error {
			_, _, err := queries.ListSubscribedClustersPage(ctx, SimplePageFilter{ScopeID: tenantID, Limit: 20})
			return err
		}},
		{"tenants", func() error { _, err := queries.ListTenantsPage(ctx, TenantListFilter{Limit: 20}); return err }},
		{"tenants/cursor", func() error {
			_, err := queries.ListTenantsPage(ctx, TenantListFilter{CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"marketplace/public", func() error {
			_, _, err := queries.ListMarketplaceClustersPage(ctx, MarketplacePageFilter{Limit: 20})
			return err
		}},
		{"marketplace/tenant-cursor", func() error {
			_, _, err := queries.ListMarketplaceClustersPage(ctx, MarketplacePageFilter{TenantID: tenantID, CursorTime: &cursorTime, CursorID: "demo-media", Backward: true, Limit: 20})
			return err
		}},
		{"marketplace/get-public", func() error { _, err := queries.GetMarketplaceCluster(ctx, "demo-media", ""); return err }},
		{"marketplace/get-tenant", func() error { _, err := queries.GetMarketplaceCluster(ctx, "demo-selfhosted", tenantID); return err }},
		{"marketplace/owner", func() error { _, err := queries.GetMarketplaceOwner(ctx, "demo-media", tenantID); return err }},
		{"marketplace/metadata", func() error { _, err := queries.ListClusterMetadata(ctx, tenantID, []string{"demo-media"}); return err }},
		{"invites/sent", func() error {
			_, _, err := queries.ListClusterInvitesPage(ctx, MembershipPageFilter{ScopeID: "demo-media", Limit: 20})
			return err
		}},
		{"invites/received-cursor", func() error {
			_, _, err := queries.ListReceivedClusterInvitesPage(ctx, MembershipPageFilter{ScopeID: tenantID, CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"subscriptions/pending", func() error {
			_, _, err := queries.ListPendingSubscriptionsPage(ctx, MembershipPageFilter{ScopeID: "demo-media", Limit: 20})
			return err
		}},
		{"nodes/public", func() error {
			_, _, err := queries.ListInfrastructureNodesPage(ctx, NodeListFilter{Scope: NodeScopePublic, Limit: 20})
			return err
		}},
		{"nodes/tenant-filtered", func() error {
			_, _, err := queries.ListInfrastructureNodesPage(ctx, NodeListFilter{Scope: NodeScopeTenant, TenantID: tenantID, ClusterID: "demo-media", NodeType: "edge", Region: "Leiden", CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"healthy-nodes/direct", func() error {
			_, _, _, err := queries.ListHealthyServiceNodes(ctx, HealthyNodeFilter{Scope: NodeScopeService, ClusterID: "central-primary", ServiceType: "foghorn", StaleThreshold: 300})
			return err
		}},
		{"healthy-nodes/assigned", func() error {
			_, _, _, err := queries.ListHealthyServiceNodes(ctx, HealthyNodeFilter{Scope: NodeScopeTenant, TenantID: tenantID, ClusterID: "demo-media", ServiceType: "foghorn", StaleThreshold: 300, Assigned: true})
			return err
		}},
		{"service-catalog", func() error { _, err := queries.ListServiceCatalog(ctx); return err }},
		{"cluster-services", func() error { _, err := queries.ListClusterServiceAssignments(ctx, "central-primary"); return err }},
		{"service-health/all", func() error { _, err := queries.ListServiceHealth(ctx, ""); return err }},
		{"service-health/filter", func() error { _, err := queries.ListServiceHealth(ctx, "foghorn"); return err }},
		{"tls-page", func() error {
			_, _, err := queries.ListTLSBundlesPage(ctx, ResourcePageFilter{ClusterID: "demo-media", CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"ingress-page", func() error {
			_, _, err := queries.ListIngressSitesPage(ctx, ResourcePageFilter{ClusterID: "demo-media", NodeID: "edge-node-1", CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"instances-page", func() error {
			_, _, err := queries.ListServiceInstancesPage(ctx, ServiceInstancePageFilter{ClusterID: "central-primary", ServiceID: "foghorn", NodeID: "central-node-1", CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"physical-instances", func() error {
			_, err := queries.ListPhysicalServiceInstances(ctx, PhysicalServiceInstanceFilter{ServiceType: "foghorn", ClusterID: "central-primary", StaleThreshold: 300})
			return err
		}},
		{"physical-domains", func() error { _, err := queries.ListProvisionedPhysicalIngressDomains(ctx); return err }},
		{"discovery/direct", func() error {
			_, err := queries.DiscoverServicesPage(ctx, ServiceDiscoveryFilter{ServiceType: "foghorn", Scope: DiscoveryScopeService, ClusterID: "central-primary", Limit: 20})
			return err
		}},
		{"discovery/pool-physical", func() error {
			_, err := queries.DiscoverServicesPage(ctx, ServiceDiscoveryFilter{ServiceType: "foghorn", TenantID: tenantID, ClusterID: "demo-media", Scope: DiscoveryScopeTenant, Pool: true, Physical: true, StaleThreshold: 300, CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"pool-status", func() error { _, err := queries.ListServicePoolStatus(ctx, "foghorn"); return err }},
		{"pool-cluster-active", func() error { _, err := queries.ServicePoolClusterActive(ctx, "demo-media"); return err }},
		{"bootstrap-tokens", func() error {
			_, err := queries.ListBootstrapTokens(ctx, BootstrapTokenFilter{Kind: &kind, TenantID: &tenantID, CursorTime: &cursorTime, CursorID: cursorID, Backward: true, Limit: 20})
			return err
		}},
		{"release-list", func() error { _, err := queries.ListEdgeReleases(ctx, EdgeReleaseFilter{}); return err }},
		{"release-target-list", func() error {
			_, err := queries.ListClusterReleaseTargets(ctx, ClusterReleaseTargetFilter{})
			return err
		}},
		{"release-target-exists", func() error { _, err := queries.EdgeReleaseTargetExists(ctx, "stable", ""); return err }},
		{"served-clusters", func() error {
			_, err := queries.ListServedClustersForNode(ctx, ServedClustersForNodeParams{Identity: "foghorn-1", ServiceType: "foghorn"})
			return err
		}},
		{"foghorn-control-cells", func() error { _, err := queries.ListFoghornControlCells(ctx, "central-primary", "", false); return err }},
		{"mesh-active-nodes", func() error { _, err := queries.ListActiveMeshNodes(ctx); return err }},
		{"mesh-service-types", func() error { _, err := queries.ListLocalMeshServiceTypes(ctx, "central-node-1"); return err }},
		{"mesh-endpoints", func() error {
			_, err := queries.ListMeshServiceEndpoints(ctx, MeshServiceEndpointParams{ClusterID: "central-primary", NodeID: "central-node-1", PeerTypes: []string{"foghorn"}, GlobalPeerTypes: []string{"bridge"}})
			return err
		}},
		{"mesh-infra-peers", func() error {
			_, err := queries.ListInfraMeshPeerNodeIDs(ctx, InfraMeshPeerParams{ClusterID: "demo-media", NodeID: "edge-node-1", Kinds: []string{"database"}, Providers: []string{"postgres"}, Names: []string{"primary"}})
			return err
		}},
		{"mesh-reciprocal-peers", func() error {
			_, err := queries.ListReciprocalMeshPeerNodeIDs(ctx, ReciprocalMeshPeerParams{NodeID: "central-node-1", ClusterID: "central-primary", ProvidedTypes: []string{"foghorn"}, DependentTypes: []string{"bridge"}, GlobalFlags: []bool{false}})
			return err
		}},
		{"mesh-peer-candidates", func() error {
			_, err := queries.ListMeshPeerCandidates(ctx, ListMeshPeerCandidatesParams{NodeID: "central-node-1", ClusterID: "central-primary", RequiredNodeIDs: []string{"edge-node-1"}})
			return err
		}},
		{"infrastructure-cluster-exists", func() error {
			_, err := queries.InfrastructureClusterExists(ctx, "central-primary")
			return err
		}},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err != nil {
				t.Fatal(err)
			}
		})
	}
	runConvertedRuntimeWriteAdapters(t, ctx, db, tenantID)
}

func runConvertedRuntimeWriteAdapters(t *testing.T, ctx context.Context, db *sql.DB, tenantID string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	queries := New(tx)

	if _, err := tx.ExecContext(ctx, `INSERT INTO quartermaster.services (service_id, name, plane, type, protocol) VALUES ('edge', 'Contract edge', 'media', 'edge', 'http') ON CONFLICT (service_id) DO NOTHING`); err != nil {
		t.Fatalf("seed write contract service: %v", err)
	}
	name, subdomain, customDomain := "Contract tenant", "contract-demo", "contract.example.com"
	logoURL, primaryColor, secondaryColor := "https://contract.example.com/logo.png", "#112233", "#445566"
	tier, deploymentModel, primaryCluster := "pro", "shared", "demo-media"
	active, monitored := true, true
	if _, err := queries.UpdateTenantFields(ctx, tenantID, TenantUpdate{Name: &name, SubdomainSet: true, Subdomain: &subdomain,
		CustomDomain: &customDomain, LogoURL: &logoURL, PrimaryColor: &primaryColor, SecondaryColor: &secondaryColor,
		DeploymentTier: &tier, DeploymentModel: &deploymentModel, PrimaryClusterID: &primaryCluster,
		IsActive: &active, MonitoringEnabled: &monitored}); err != nil {
		t.Fatalf("update tenant fields: %v", err)
	}
	clusterName, baseURL, health := "Contract media", "contract-media.example.com", "healthy"
	if _, err := queries.UpdateClusterFields(ctx, "demo-media", ClusterUpdate{ClusterName: &clusterName, BaseURL: &baseURL,
		HealthStatus: &health, IsActive: &active, OwnerTenantIDSet: true, OwnerTenantID: &tenantID,
		DeploymentModel: &deploymentModel, IsPlatformOfficial: &active, IsDefaultCluster: &active,
		AllowPrivatePullSources: &active, PublicTopology: &active}); err != nil {
		t.Fatalf("update cluster fields: %v", err)
	}
	visibility, class, description := "public", "tenant_private", "Contract marketplace description"
	if err := queries.UpdateMarketplace(ctx, UpdateMarketplaceParams{ClusterID: "demo-media", Visibility: &visibility,
		ClusterClass: &class, RequiresApproval: &active, ShortDescription: &description}); err != nil {
		t.Fatalf("update marketplace: %v", err)
	}

	now := time.Now().UTC()
	internalIP, externalIP, wireguardIP, publicKey := "10.90.0.2", "192.0.2.20", "10.91.0.2", "contract-public-key"
	region, zone := "eu-contract", "eu-contract-1"
	cpu, memory, disk := int32(8), int32(32), int32(512)
	if err := queries.UpsertInfrastructureNode(ctx, UpsertInfrastructureNodeParams{NodeID: "contract-runtime-node", ClusterID: "demo-media",
		NodeName: "Contract runtime node", NodeType: "edge", InternalIP: &internalIP, ExternalIP: &externalIP,
		WireguardIP: &wireguardIP, WireguardPublicKey: &publicKey, WireguardListenPort: int32(51820), Region: &region,
		AvailabilityZone: &zone, Latitude: 52.1, Longitude: 4.3, CPUCores: &cpu, MemoryGB: &memory, DiskGB: &disk, Now: now}); err != nil {
		t.Fatalf("upsert infrastructure node: %v", err)
	}
	if _, err := queries.GetExistingInfrastructureNode(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get existing infrastructure node: %v", err)
	}
	nodeIdentityKey := make([]byte, 32)
	if err := queries.UpsertEdgeNodeFingerprint(ctx, UpsertEdgeNodeFingerprintParams{TenantID: tenantID, NodeID: "contract-runtime-node",
		MachineIDSHA: "contract-machine", MACsSHA: "contract-macs", AttrsJSON: `{"contract":true}`, IPs: []string{"192.0.2.20"},
		PublicKeyEd25519: nodeIdentityKey}); err != nil {
		t.Fatalf("upsert edge fingerprint: %v", err)
	}
	if _, err := queries.BindNodeFingerprintPublicKey(ctx, "contract-runtime-node", nodeIdentityKey); err != nil {
		t.Fatalf("bind edge fingerprint public key: %v", err)
	}
	if _, err := queries.GetEdgeNodeFingerprintBindingForUpdate(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get edge fingerprint binding: %v", err)
	}
	rotatedNodeIdentityKey := make([]byte, 32)
	rotatedNodeIdentityKey[0] = 1
	if err := queries.RotateEdgeNodeIdentityKey(ctx, tenantID, "contract-runtime-node", "contract-machine", "contract-macs", rotatedNodeIdentityKey); err != nil {
		t.Fatalf("rotate edge fingerprint public key: %v", err)
	}
	rotatedBinding, err := queries.GetEdgeNodeFingerprintBindingForUpdate(ctx, "contract-runtime-node")
	if err != nil || !bytes.Equal(rotatedBinding.PublicKeyEd25519, rotatedNodeIdentityKey) {
		t.Fatalf("rotated edge fingerprint binding mismatch: %+v err=%v", rotatedBinding, err)
	}
	if err := queries.UpsertFingerprintSeenIP(ctx, "contract-runtime-node", "192.0.2.21"); err != nil {
		t.Fatalf("upsert fingerprint seen IP: %v", err)
	}
	for _, lookup := range []struct {
		kind  NodeFingerprintLookup
		value string
	}{{NodeFingerprintByMachineID, "contract-machine"}, {NodeFingerprintByMACs, "contract-macs"}, {NodeFingerprintBySeenIP, "192.0.2.21"}} {
		if _, err := queries.ResolveNodeFingerprint(ctx, lookup.kind, lookup.value); err != nil {
			t.Fatalf("resolve node fingerprint kind %d: %v", lookup.kind, err)
		}
	}
	if _, err := queries.GetClusterMeshConfig(ctx, "demo-media"); err != nil {
		t.Fatalf("get cluster mesh config: %v", err)
	}
	if _, err := queries.GetMeshNodeIdentity(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get mesh node identity: %v", err)
	}
	if err := queries.UpdateMeshHeartbeatWithSnapshot(ctx, UpdateMeshHeartbeatParams{NodeID: "contract-runtime-node",
		AppliedRevision: sql.NullString{String: "7", Valid: true}, SnapshotCPU: sql.NullFloat64{Float64: 12.5, Valid: true},
		SnapshotRamUsed: sql.NullInt64{Int64: 1024, Valid: true}, SnapshotRamTotal: sql.NullInt64{Int64: 2048, Valid: true},
		SnapshotDiskUsed: sql.NullInt64{Int64: 4096, Valid: true}, SnapshotDiskTotal: sql.NullInt64{Int64: 8192, Valid: true},
		SnapshotUptime: sql.NullInt64{Int64: 60, Valid: true}, SnapshotAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
		t.Fatalf("update mesh heartbeat snapshot: %v", err)
	}
	if err := queries.StoreMeshNodeConfig(ctx, StoreMeshNodeConfigParams{NodeID: "contract-runtime-node", ClusterID: "demo-media",
		MeshRevision: "7", TopologySourceHash: "contract-hash", WireguardIP: wireguardIP, WireguardPort: 51820,
		PeersJSON: `[]`, ServiceEndpointsJSON: `{}`}); err != nil {
		t.Fatalf("store mesh node config: %v", err)
	}
	if err := queries.UpdateMeshHeartbeat(ctx, "contract-runtime-node", sql.NullString{String: "8", Valid: true}); err != nil {
		t.Fatalf("update mesh heartbeat: %v", err)
	}
	if _, err := queries.CurrentMeshTopologyRevision(ctx); err != nil {
		t.Fatalf("current mesh topology revision: %v", err)
	}
	if _, err := queries.GetMeshNodeConfig(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get mesh node config: %v", err)
	}
	revision, err := queries.ClaimMeshTopologyWarm(ctx, "contract-planner")
	if err != nil {
		t.Fatalf("claim mesh topology warm: %v", err)
	}
	if err := queries.CompleteMeshTopologyWarm(ctx, revision, "contract-planner"); err != nil {
		t.Fatalf("complete mesh topology warm: %v", err)
	}
	if err := queries.ReleaseMeshTopologyWarm(ctx); err != nil {
		t.Fatalf("release mesh topology warm: %v", err)
	}
	if _, err := queries.UpdateNodeHardwareRecord(ctx, UpdateNodeHardwareRecordParams{NodeID: "contract-runtime-node", CpuCores: &cpu, MemoryGB: &memory, DiskGB: &disk}); err != nil {
		t.Fatalf("update node hardware: %v", err)
	}
	if _, err := queries.UpdateNodeStatus(ctx, UpdateNodeStatusParams{NodeID: "contract-runtime-node", Status: "active", ExpectedClusterID: "demo-media", Scope: NodeStatusScopeTenantOwner, TenantID: tenantID}); err != nil {
		t.Fatalf("update node status: %v", err)
	}
	if _, err := queries.TenantIsProvider(ctx, tenantID); err != nil {
		t.Fatalf("tenant provider authorization: %v", err)
	}
	if _, err := queries.ActiveClusterExists(ctx, "demo-media"); err != nil {
		t.Fatalf("active cluster authorization: %v", err)
	}
	if _, err := queries.TenantHasClusterLifecycleAccess(ctx, TenantHasClusterLifecycleAccessParams{TenantID: tenantID, ClusterID: "demo-media"}); err != nil {
		t.Fatalf("tenant cluster lifecycle authorization: %v", err)
	}

	domainsJSON, metadataJSON := `["contract-media.example.com"]`, `{"contract":true}`
	if _, err := queries.UpsertTLSBundle(ctx, UpsertTLSBundleParams{BundleID: "contract-bundle", ClusterID: "demo-media",
		Issuer: "letsencrypt", Email: "ops@example.com", DomainsJSON: &domainsJSON, MetadataJSON: &metadataJSON}); err != nil {
		t.Fatalf("upsert TLS bundle: %v", err)
	}
	if _, err := queries.UpsertIngressSite(ctx, UpsertIngressSiteParams{SiteID: "contract-site", ClusterID: "demo-media",
		NodeID: "contract-runtime-node", TLSBundleID: "contract-bundle", Kind: "physical", Upstream: "http://192.0.2.20:8080",
		DomainsJSON: &domainsJSON, MetadataJSON: &metadataJSON}); err != nil {
		t.Fatalf("upsert ingress site: %v", err)
	}

	tokenID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	usageLimit := int32(2)
	if err := queries.CreateBootstrapToken(ctx, CreateBootstrapTokenParams{ID: tokenID, Name: "Contract token", TokenHash: "contract-token-hash",
		TokenPrefix: "contract", Kind: "edge_node", TenantID: &tenantID, ClusterID: &primaryCluster, Metadata: `{"contract":true}`,
		UsageLimit: &usageLimit, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("create bootstrap token: %v", err)
	}
	if _, err := queries.ConsumeBootstrapToken(ctx, "contract-token-hash"); err != nil {
		t.Fatalf("consume bootstrap token: %v", err)
	}
	if _, err := queries.GetBootstrapTokenForValidation(ctx, "contract-token-hash"); err != nil {
		t.Fatalf("validate bootstrap token: %v", err)
	}
	if _, err := queries.LockEdgeEnrollmentToken(ctx, "contract-token-hash"); err != nil {
		t.Fatalf("lock edge enrollment token: %v", err)
	}
	infraTokenID, infraTokenHash, infraKind := "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "contract-infra-token", "infrastructure_node"
	if err := queries.CreateBootstrapToken(ctx, CreateBootstrapTokenParams{ID: infraTokenID, Name: "Contract infrastructure token",
		TokenHash: infraTokenHash, TokenPrefix: "contract-i", Kind: infraKind, TenantID: &tenantID, ClusterID: &primaryCluster,
		Metadata: `{"node_type":"core"}`, UsageLimit: &usageLimit, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("create infrastructure bootstrap token: %v", err)
	}
	if _, err := queries.LockInfrastructureEnrollmentToken(ctx, infraTokenHash); err != nil {
		t.Fatalf("lock infrastructure enrollment token: %v", err)
	}
	if _, err := queries.FirstActiveCluster(ctx); err != nil {
		t.Fatalf("first active cluster: %v", err)
	}
	if _, err := queries.GetNodeCluster(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get node cluster: %v", err)
	}
	if err := queries.IncrementBootstrapTokenUsage(ctx, infraTokenID); err != nil {
		t.Fatalf("increment bootstrap token usage: %v", err)
	}
	if err := queries.MergeInfrastructureNodeMetadata(ctx, "contract-runtime-node", `{"merged":true}`); err != nil {
		t.Fatalf("merge infrastructure node metadata: %v", err)
	}
	if err := queries.UpdateNodeEnrollmentOrigin(ctx, "contract-runtime-node", "runtime_enrolled"); err != nil {
		t.Fatalf("update node enrollment origin: %v", err)
	}
	if _, err := queries.GetNodeEnrollmentOrigin(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get node enrollment origin: %v", err)
	}
	if _, err := queries.GetBootstrapReplayToken(ctx, infraTokenHash); err != nil {
		t.Fatalf("get bootstrap replay token: %v", err)
	}
	if _, err := queries.GetBootstrapReplayNode(ctx, "contract-runtime-node"); err != nil {
		t.Fatalf("get bootstrap replay node: %v", err)
	}
	if err := queries.CreateEdgeNode(ctx, CreateEdgeNodeParams{ID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", NodeID: "contract-edge-node",
		ClusterID: "demo-media", Hostname: "contract-edge-node", ExternalIP: "192.0.2.30", Latitude: 52.2, Longitude: 4.4}); err != nil {
		t.Fatalf("create edge node: %v", err)
	}
	if err := queries.CreateInfrastructureNode(ctx, CreateInfrastructureNodeParams{ID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
		NodeID: "contract-infra-node", ClusterID: "central-primary", Hostname: "contract-infra-node", NodeType: "core",
		ExternalIP: "192.0.2.31", InternalIP: "10.90.0.31", WireguardIP: "10.91.0.31", WireguardPublicKey: "contract-infra-key",
		WireguardPort: 51820, Latitude: 52.3, Longitude: 4.5, Metadata: `{"contract":true}`}); err != nil {
		t.Fatalf("create infrastructure node: %v", err)
	}

	if _, err := queries.UpsertEdgeRelease(ctx, UpsertEdgeReleaseParams{Channel: "rc", Version: "v1.2.3", ComponentsJSON: `{"edge":"v1.2.3"}`, PublishedAt: now}); err != nil {
		t.Fatalf("upsert edge release: %v", err)
	}
	if _, err := queries.UpsertClusterReleaseTarget(ctx, UpsertClusterReleaseTargetParams{ClusterID: "demo-media", Channel: "rc",
		TargetVersion: "v1.2.3", RolloutPlanJSON: `{"batch":1}`, Paused: false}); err != nil {
		t.Fatalf("upsert cluster release target: %v", err)
	}
	if _, err := queries.GetClusterReleaseTarget(ctx, "demo-media"); err != nil {
		t.Fatalf("get cluster release target: %v", err)
	}

	privateClusterID := "contract-private"
	if err := queries.CreatePrivateInfrastructureCluster(ctx, CreatePrivateInfrastructureClusterParams{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		ClusterID: privateClusterID, ClusterName: "Contract private", OwnerTenantID: tenantID, ShortDescription: &description,
		CreatedAt: now, ControlCellID: "central-primary", RegionID: "eu-contract", BaseURL: "private.contract.example.com"}); err != nil {
		t.Fatalf("create private cluster: %v", err)
	}
	if err := queries.GrantPrivateClusterOwnerAccess(ctx, GrantPrivateClusterOwnerAccessParams{TenantID: tenantID, ClusterID: privateClusterID}); err != nil {
		t.Fatalf("grant private cluster access: %v", err)
	}
	foghornID := "5eedf0e1-0001-da7a-f0e1-0001da7a0001"
	if err := queries.AssignRuntimeFoghornToPrivateCluster(ctx, AssignRuntimeFoghornToPrivateClusterParams{ServiceInstanceID: foghornID, ClusterID: privateClusterID}); err != nil {
		t.Fatalf("assign runtime foghorn: %v", err)
	}
	if err := queries.AssignFoghornToPrivateCluster(ctx, AssignFoghornToPrivateClusterParams{ServiceInstanceID: foghornID, ClusterID: privateClusterID}); err != nil {
		t.Fatalf("assign foghorn: %v", err)
	}
	if err := queries.CreateEdgeBootstrapTokenRecord(ctx, CreateEdgeBootstrapTokenRecordParams{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		TokenHash: "contract-private-edge-token", TokenPrefix: "contract-p", Name: "Contract private edge", TenantID: tenantID,
		ClusterID: sql.NullString{String: privateClusterID, Valid: true}, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("create private edge bootstrap token: %v", err)
	}
	if _, err := queries.GetClusterPublicRootDomain(ctx, privateClusterID); err != nil {
		t.Fatalf("get cluster public root domain: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quartermaster.service_instances SET health_status = 'healthy', last_health_check = NOW() WHERE id = $1::uuid`, foghornID); err != nil {
		t.Fatalf("mark fixture foghorn healthy: %v", err)
	}
	if _, err := queries.GetClusterInternalFoghornGRPC(ctx, privateClusterID); err != nil {
		t.Fatalf("get cluster internal foghorn: %v", err)
	}

	if err := queries.UpdateReportedNodes(ctx, UpdateReportedNodesParams{NodeIDs: []string{"contract-runtime-node"}, ExternalIPs: []string{"192.0.2.22"}, RefreshHeartbeat: []bool{true}}); err != nil {
		t.Fatalf("update reported nodes: %v", err)
	}
	if err := queries.UpsertHealthyEdgeInstance(ctx, UpsertHealthyEdgeInstanceParams{InstanceID: "contract-edge-instance", NodeID: "contract-runtime-node", ServiceType: "edge"}); err != nil {
		t.Fatalf("upsert healthy edge instance: %v", err)
	}
	if err := queries.MarkEdgeInstanceUnhealthy(ctx, MarkEdgeInstanceUnhealthyParams{ServiceType: "edge", NodeID: "contract-runtime-node"}); err != nil {
		t.Fatalf("mark edge instance unhealthy: %v", err)
	}
	if _, err := queries.ListPriorNodeStates(ctx, []string{"contract-runtime-node"}); err != nil {
		t.Fatalf("list prior node states: %v", err)
	}
	if _, err := queries.ListPriorEdgeInstanceStates(ctx, []string{"contract-runtime-node"}); err != nil {
		t.Fatalf("list prior edge instance states: %v", err)
	}
	if _, err := queries.AssignServicePoolCount(ctx, AssignServicePoolCountParams{ClusterID: privateClusterID, Count: 1, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("assign service pool count: %v", err)
	}
	if _, err := queries.AssignServicePoolInstance(ctx, AssignServicePoolInstanceParams{ClusterID: privateClusterID, InstanceID: foghornID, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("assign service pool instance: %v", err)
	}
	if _, err := queries.ReleaseOldestServicePoolInstances(ctx, ReleaseOldestServicePoolParams{ClusterID: privateClusterID, Count: 1, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("release oldest service pool instance: %v", err)
	}
	if _, err := queries.AssignServicePoolInstance(ctx, AssignServicePoolInstanceParams{ClusterID: privateClusterID, InstanceID: foghornID, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("reassign service pool instance: %v", err)
	}
	if _, _, err := queries.DrainServicePoolInstance(ctx, ServicePoolInstanceParams{InstanceID: foghornID, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("drain service pool instance: %v", err)
	}
	if _, err := queries.AssignServicePoolInstance(ctx, AssignServicePoolInstanceParams{ClusterID: privateClusterID, InstanceID: foghornID, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("reassign drained service pool instance: %v", err)
	}
	if _, _, err := queries.ReleaseServicePoolInstances(ctx, ServicePoolInstancesParams{InstanceIDs: []string{foghornID}, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("release service pool instances: %v", err)
	}
	if err := queries.UnassignServicePoolInstances(ctx, UnassignServicePoolParams{ClusterID: privateClusterID, InstanceIDs: []string{foghornID}, ServiceType: "foghorn"}); err != nil {
		t.Fatalf("unassign service pool instances: %v", err)
	}
	if _, err := queries.RevokeBootstrapToken(ctx, tokenID); err != nil {
		t.Fatalf("revoke bootstrap token: %v", err)
	}
}

func mustServiceInstanceID(t *testing.T, tx *sql.Tx, instanceID string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(`SELECT id::text FROM quartermaster.service_instances WHERE instance_id = $1`, instanceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type quartermasterGeneratedQuery struct {
	file string
	name string
	sql  string
}

func quartermasterGeneratedQueries(t *testing.T) []quartermasterGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []quartermasterGeneratedQuery
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					queryName := "unknown"
					if index < len(value.Names) {
						queryName = value.Names[index].Name
					}
					queries = append(queries, quartermasterGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startQuartermasterQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-quartermaster-query-catalog-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
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
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func startQuartermasterQueryCatalogRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "quartermaster"); ok {
		schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return db
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-quartermaster-query-catalog-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--hostname", name, "-P", image,
		"bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
