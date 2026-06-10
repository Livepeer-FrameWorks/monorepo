package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// qmR builds a resolver backed only by a FakeQuartermaster. A non-stubbed QM
// method panics, which proves a guard short-circuited before the backend call.
func qmR(qm *clientstest.FakeQuartermaster) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithQuartermaster(qm)), Logger: clientstest.DiscardLogger()}
}

// qmUserCtx layers a *middleware.UserContext onto an authed (non-demo) context.
// Several infra resolvers read the tenant via middleware.GetUserFromContext
// rather than the ctxkeys fallback, so AuthedCtx alone leaves their tenant
// empty; this seam drives their real path.
func qmUserCtx(tenantID string) context.Context {
	ctx := clientstest.AuthedCtx(tenantID)
	return context.WithValue(ctx, ctxkeys.KeyUser, &middleware.UserContext{TenantID: tenantID})
}

// ---- DoGetTenant: ctxkeys tenant guard, unwraps resp.Tenant ----

func TestDoGetTenant_HappyAndGuards(t *testing.T) {
	var gotTenantID string
	qm := &clientstest.FakeQuartermaster{
		GetTenantFn: func(_ context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
			gotTenantID = tenantID
			return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: "t1", Name: "Acme"}}, nil
		},
	}
	r := qmR(qm)

	got, err := r.DoGetTenant(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatalf("DoGetTenant: %v", err)
	}
	// Resolver forwards the context tenant ID to the backend and unwraps Tenant.
	if gotTenantID != "t1" {
		t.Fatalf("forwarded tenant = %q, want t1", gotTenantID)
	}
	if got.GetName() != "Acme" {
		t.Fatalf("tenant name = %q, want Acme", got.GetName())
	}

	// No tenant in context: guard short-circuits before any backend call.
	noTenant := &clientstest.FakeQuartermaster{}
	if _, err := qmR(noTenant).DoGetTenant(context.Background()); err == nil {
		t.Fatal("expected tenant-required error")
	}
	if noTenant.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", noTenant.Calls)
	}
}

func TestDoGetTenant_NotFoundAndError(t *testing.T) {
	// resp.Tenant nil maps to a "not found" error even on a nil backend error.
	nilTenant := &clientstest.FakeQuartermaster{
		GetTenantFn: func(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
			return &quartermasterpb.GetTenantResponse{}, nil
		},
	}
	if _, err := qmR(nilTenant).DoGetTenant(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("expected not-found error for nil Tenant")
	}

	failing := &clientstest.FakeQuartermaster{
		GetTenantFn: func(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoGetTenant(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// ---- DoGetClusters: requires owned cluster, returns resp.Clusters ----

func TestDoGetClusters_HappyAndGuard(t *testing.T) {
	var gotOwner string
	var gotPag *commonpb.CursorPaginationRequest
	qm := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(_ context.Context, owner string, pag *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			gotOwner = owner
			gotPag = pag
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{{Id: "c1", ClusterId: "c1", ClusterName: "Edge"}},
			}, nil
		},
	}
	first := 5
	got, err := qmR(qm).DoGetClusters(clientstest.AuthedCtx("t1"), &first, nil)
	if err != nil {
		t.Fatalf("DoGetClusters: %v", err)
	}
	if gotOwner != "t1" {
		t.Fatalf("owner = %q, want t1", gotOwner)
	}
	// requireClusterOperatorTenant calls ListClustersByOwner first (with the max
	// limit), so the user-supplied `first` lands on the SECOND call's pagination.
	if gotPag.GetFirst() != int32(first) {
		t.Fatalf("first = %d, want %d", gotPag.GetFirst(), first)
	}
	if len(got) != 1 || got[0].GetClusterName() != "Edge" {
		t.Fatalf("unexpected clusters: %+v", got)
	}

	// Tenant present but owns nothing: cluster-operator gate rejects.
	empty := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
	}
	if _, err := qmR(empty).DoGetClusters(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("expected cluster-owner-required error")
	}

	// No tenant: guard short-circuits before any backend call.
	noTenant := &clientstest.FakeQuartermaster{}
	if _, err := qmR(noTenant).DoGetClusters(context.Background(), nil, nil); err == nil {
		t.Fatal("expected tenant-required error")
	}
	if noTenant.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", noTenant.Calls)
	}
}

// ---- DoGetCluster: GetCluster then requireOwnedCluster ----

func TestDoGetCluster_HappyAndOwnershipGate(t *testing.T) {
	var gotClusterID string
	qm := &clientstest.FakeQuartermaster{
		GetClusterFn: func(_ context.Context, id string) (*quartermasterpb.ClusterResponse, error) {
			gotClusterID = id
			return &quartermasterpb.ClusterResponse{
				Cluster: &quartermasterpb.InfrastructureCluster{Id: "c1", ClusterId: "c1", ClusterName: "Edge"},
			}, nil
		},
		// requireOwnedCluster("c1") must find c1 among owned clusters.
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}},
			}, nil
		},
	}
	got, err := qmR(qm).DoGetCluster(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoGetCluster: %v", err)
	}
	if gotClusterID != "c1" {
		t.Fatalf("looked up %q, want c1", gotClusterID)
	}
	if got.GetClusterName() != "Edge" {
		t.Fatalf("cluster name = %q, want Edge", got.GetClusterName())
	}

	// Cluster fetched but not owned by tenant: ownership gate rejects.
	notOwned := &clientstest.FakeQuartermaster{
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "c1"}}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "other"}}}, nil
		},
	}
	if _, err := qmR(notOwned).DoGetCluster(qmUserCtx("t1"), "c1"); err == nil {
		t.Fatal("expected ownership gate to reject unowned cluster")
	}

	// Backend error on GetCluster propagates.
	failing := &clientstest.FakeQuartermaster{
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoGetCluster(qmUserCtx("t1"), "c1"); err == nil {
		t.Fatal("expected GetCluster error to propagate")
	}
}

// ---- DoGetNodes: cluster-scoped vs owned-fan-out, ID normalization ----

func TestDoGetNodes_ClusterScopedNormalizesGlobalID(t *testing.T) {
	var gotCluster, gotType string
	qm := &clientstest.FakeQuartermaster{
		// Ownership check for the supplied cluster filter.
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
		ListNodesFn: func(_ context.Context, clusterID, nodeType, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			gotCluster, gotType = clusterID, nodeType
			return &quartermasterpb.ListNodesResponse{
				Nodes: []*quartermasterpb.InfrastructureNode{{Id: "n1", ClusterId: "c1"}},
			}, nil
		},
	}
	// A Relay global ID for cluster c1 must decode to the raw ID before filtering.
	gid := globalid.Encode(globalid.TypeCluster, "c1")
	typeArg := "edge"
	got, err := qmR(qm).DoGetNodes(qmUserCtx("t1"), &gid, nil, &typeArg, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetNodes: %v", err)
	}
	if gotCluster != "c1" {
		t.Fatalf("ListNodes cluster filter = %q, want decoded c1", gotCluster)
	}
	if gotType != "edge" {
		t.Fatalf("ListNodes type filter = %q, want edge", gotType)
	}
	if len(got) != 1 || got[0].GetId() != "n1" {
		t.Fatalf("unexpected nodes: %+v", got)
	}
}

func TestDoGetNodes_FanOutOverOwnedClusters(t *testing.T) {
	listNodesCalls := map[string]int{}
	qm := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}, {ClusterId: "c2"}}}, nil
		},
		ListNodesFn: func(_ context.Context, clusterID, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			listNodesCalls[clusterID]++
			return &quartermasterpb.ListNodesResponse{Nodes: []*quartermasterpb.InfrastructureNode{{Id: "n-" + clusterID, ClusterId: clusterID}}}, nil
		},
	}
	// No cluster filter: resolver enumerates owned clusters and concatenates nodes.
	got, err := qmR(qm).DoGetNodes(qmUserCtx("t1"), nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetNodes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 nodes (one per owned cluster), got %d", len(got))
	}
	if listNodesCalls["c1"] != 1 || listNodesCalls["c2"] != 1 {
		t.Fatalf("expected one ListNodes per owned cluster, got %+v", listNodesCalls)
	}
}

// ---- DoGetNode: requireOwnedNode unwraps node then checks ownership ----

func TestDoGetNode_HappyAndUnownedNode(t *testing.T) {
	var gotNodeID string
	qm := &clientstest.FakeQuartermaster{
		GetNodeFn: func(_ context.Context, nodeID string) (*quartermasterpb.NodeResponse, error) {
			gotNodeID = nodeID
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{Id: "n1", ClusterId: "c1"}}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
	}
	got, err := qmR(qm).DoGetNode(qmUserCtx("t1"), "n1")
	if err != nil {
		t.Fatalf("DoGetNode: %v", err)
	}
	if gotNodeID != "n1" || got.GetId() != "n1" {
		t.Fatalf("unexpected node: looked up %q, got %+v", gotNodeID, got)
	}

	// Node belongs to a cluster the tenant does not own: ownership gate rejects.
	unowned := &clientstest.FakeQuartermaster{
		GetNodeFn: func(context.Context, string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{Id: "n1", ClusterId: "c9"}}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
	}
	if _, err := qmR(unowned).DoGetNode(qmUserCtx("t1"), "n1"); err == nil {
		t.Fatal("expected ownership gate to reject node in unowned cluster")
	}
}

// ---- DoGetServiceInstances: node filter resolves to its cluster ----

func TestDoGetServiceInstances_NodeFilterResolvesCluster(t *testing.T) {
	var gotCluster, gotNode string
	qm := &clientstest.FakeQuartermaster{
		// requireOwnedNode: GetNode then ownership check.
		GetNodeFn: func(context.Context, string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{Id: "n1", ClusterId: "c1"}}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
		ListServiceInstancesFn: func(_ context.Context, clusterID, _, nodeID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
			gotCluster, gotNode = clusterID, nodeID
			return &quartermasterpb.ListServiceInstancesResponse{
				Instances: []*quartermasterpb.ServiceInstance{{InstanceId: "i1", ServiceId: "helmsman", ClusterId: "c1"}},
			}, nil
		},
	}
	nodeGID := globalid.Encode(globalid.TypeInfrastructureNode, "n1")
	got, err := qmR(qm).DoGetServiceInstances(qmUserCtx("t1"), nil, &nodeGID, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetServiceInstances: %v", err)
	}
	// The node's cluster becomes the cluster filter; node filter is the raw node ID.
	if gotCluster != "c1" {
		t.Fatalf("cluster filter = %q, want c1 (from node)", gotCluster)
	}
	if gotNode != "n1" {
		t.Fatalf("node filter = %q, want decoded n1", gotNode)
	}
	if len(got) != 1 || got[0].GetServiceId() != "helmsman" {
		t.Fatalf("unexpected instances: %+v", got)
	}
}

// ---- DoDiscoverServices: cluster-scoped passes serviceType + cluster ----

func TestDoDiscoverServices_ClusterScopedAndError(t *testing.T) {
	var gotType, gotCluster string
	qm := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
		DiscoverServicesFn: func(_ context.Context, serviceType, clusterID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
			gotType, gotCluster = serviceType, clusterID
			return &quartermasterpb.ServiceDiscoveryResponse{
				Instances: []*quartermasterpb.ServiceInstance{{InstanceId: "i1", ServiceId: "foghorn"}},
			}, nil
		},
	}
	cluster := "c1"
	got, err := qmR(qm).DoDiscoverServices(qmUserCtx("t1"), "foghorn", &cluster, nil, nil)
	if err != nil {
		t.Fatalf("DoDiscoverServices: %v", err)
	}
	if gotType != "foghorn" || gotCluster != "c1" {
		t.Fatalf("DiscoverServices got (%q,%q), want (foghorn,c1)", gotType, gotCluster)
	}
	if len(got) != 1 || got[0].GetInstanceId() != "i1" {
		t.Fatalf("unexpected instances: %+v", got)
	}

	// Backend error propagates.
	failing := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
		DiscoverServicesFn: func(context.Context, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoDiscoverServices(qmUserCtx("t1"), "foghorn", &cluster, nil, nil); err == nil {
		t.Fatal("expected DiscoverServices error to propagate")
	}
}

// ---- DoListMySubscriptions: GetUserFromContext tenant guard ----

func TestDoListMySubscriptions_HappyAndGuard(t *testing.T) {
	var gotReq *quartermasterpb.ListMySubscriptionsRequest
	qm := &clientstest.FakeQuartermaster{
		ListMySubscriptionsFn: func(_ context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			gotReq = req
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1", ClusterName: "Shared"}},
			}, nil
		},
	}
	first := 3
	got, err := qmR(qm).DoListMySubscriptions(qmUserCtx("t1"), &first, nil)
	if err != nil {
		t.Fatalf("DoListMySubscriptions: %v", err)
	}
	if gotReq.GetTenantId() != "t1" {
		t.Fatalf("req tenant = %q, want t1", gotReq.GetTenantId())
	}
	if gotReq.GetPagination().GetFirst() != int32(first) {
		t.Fatalf("req first = %d, want %d", gotReq.GetPagination().GetFirst(), first)
	}
	if len(got) != 1 || got[0].GetClusterName() != "Shared" {
		t.Fatalf("unexpected subscriptions: %+v", got)
	}

	// AuthedCtx alone (no UserContext) leaves the user-derived tenant empty: guard fires.
	guard := &clientstest.FakeQuartermaster{}
	if _, err := qmR(guard).DoListMySubscriptions(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("expected tenant-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoListClusterInvites: owner-scoped request; no tenant guard ----

func TestDoListClusterInvites_PassesClusterAndOwner(t *testing.T) {
	var gotReq *quartermasterpb.ListClusterInvitesRequest
	qm := &clientstest.FakeQuartermaster{
		ListClusterInvitesFn: func(_ context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
			gotReq = req
			return &quartermasterpb.ListClusterInvitesResponse{
				Invites: []*quartermasterpb.ClusterInvite{{Id: "inv1", ClusterId: "c1", Status: "pending"}},
			}, nil
		},
	}
	got, err := qmR(qm).DoListClusterInvites(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoListClusterInvites: %v", err)
	}
	if gotReq.GetClusterId() != "c1" || gotReq.GetOwnerTenantId() != "t1" {
		t.Fatalf("req = (cluster %q, owner %q), want (c1, t1)", gotReq.GetClusterId(), gotReq.GetOwnerTenantId())
	}
	if len(got) != 1 || got[0].GetId() != "inv1" {
		t.Fatalf("unexpected invites: %+v", got)
	}

	failing := &clientstest.FakeQuartermaster{
		ListClusterInvitesFn: func(context.Context, *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoListClusterInvites(qmUserCtx("t1"), "c1"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// ---- DoListMyClusterInvites: GetUserFromContext tenant guard ----

func TestDoListMyClusterInvites_HappyAndGuard(t *testing.T) {
	var gotReq *quartermasterpb.ListMyClusterInvitesRequest
	qm := &clientstest.FakeQuartermaster{
		ListMyClusterInvitesFn: func(_ context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
			gotReq = req
			return &quartermasterpb.ListClusterInvitesResponse{
				Invites: []*quartermasterpb.ClusterInvite{{Id: "inv1", InvitedTenantId: "t1"}},
			}, nil
		},
	}
	got, err := qmR(qm).DoListMyClusterInvites(qmUserCtx("t1"))
	if err != nil {
		t.Fatalf("DoListMyClusterInvites: %v", err)
	}
	if gotReq.GetTenantId() != "t1" {
		t.Fatalf("req tenant = %q, want t1", gotReq.GetTenantId())
	}
	if len(got) != 1 || got[0].GetId() != "inv1" {
		t.Fatalf("unexpected invites: %+v", got)
	}

	guard := &clientstest.FakeQuartermaster{}
	if _, err := qmR(guard).DoListMyClusterInvites(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("expected tenant-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoListPendingSubscriptions: owner-scoped request; no tenant guard ----

func TestDoListPendingSubscriptions_PassesClusterAndOwner(t *testing.T) {
	var gotReq *quartermasterpb.ListPendingSubscriptionsRequest
	qm := &clientstest.FakeQuartermaster{
		ListPendingSubscriptionsFn: func(_ context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error) {
			gotReq = req
			return &quartermasterpb.ListPendingSubscriptionsResponse{
				Subscriptions: []*quartermasterpb.ClusterSubscription{{Id: "sub1", ClusterId: "c1", TenantId: "t9"}},
			}, nil
		},
	}
	got, err := qmR(qm).DoListPendingSubscriptions(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoListPendingSubscriptions: %v", err)
	}
	if gotReq.GetClusterId() != "c1" || gotReq.GetOwnerTenantId() != "t1" {
		t.Fatalf("req = (cluster %q, owner %q), want (c1, t1)", gotReq.GetClusterId(), gotReq.GetOwnerTenantId())
	}
	if len(got) != 1 || got[0].GetId() != "sub1" {
		t.Fatalf("unexpected subscriptions: %+v", got)
	}

	failing := &clientstest.FakeQuartermaster{
		ListPendingSubscriptionsFn: func(context.Context, *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoListPendingSubscriptions(qmUserCtx("t1"), "c1"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// ---- DoGetClustersAvailable: maps AvailableClusterEntry -> model.AvailableCluster ----

func TestDoGetClustersAvailable_MapsEntries(t *testing.T) {
	var gotPag *commonpb.CursorPaginationRequest
	qm := &clientstest.FakeQuartermaster{
		ListClustersAvailableFn: func(_ context.Context, pag *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error) {
			gotPag = pag
			return &quartermasterpb.ClustersAvailableResponse{
				Clusters: []*quartermasterpb.AvailableClusterEntry{
					{ClusterId: "c1", ClusterName: "US West", Tiers: []string{"free", "pro"}, AutoEnroll: true},
				},
			}, nil
		},
	}
	first := 7
	got, err := qmR(qm).DoGetClustersAvailable(clientstest.AuthedCtx("t1"), &first, nil)
	if err != nil {
		t.Fatalf("DoGetClustersAvailable: %v", err)
	}
	if gotPag.GetFirst() != int32(first) {
		t.Fatalf("pagination first = %d, want %d", gotPag.GetFirst(), first)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 available cluster, got %d", len(got))
	}
	want := &model.AvailableCluster{ClusterID: "c1", ClusterName: "US West", Tiers: []string{"free", "pro"}, AutoEnroll: true}
	g := got[0]
	if g.ClusterID != want.ClusterID || g.ClusterName != want.ClusterName || g.AutoEnroll != want.AutoEnroll || len(g.Tiers) != 2 {
		t.Fatalf("mapped cluster = %+v, want %+v", g, want)
	}

	failing := &clientstest.FakeQuartermaster{
		ListClustersAvailableFn: func(context.Context, *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoGetClustersAvailable(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("expected error to propagate")
	}
}

// ---- DoGetClustersAccess: merges access list with owned (owner override) ----

func TestDoGetClustersAccess_MergesAndMarksOwner(t *testing.T) {
	qm := &clientstest.FakeQuartermaster{
		ListClustersForTenantFn: func(_ context.Context, tenantID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error) {
			if tenantID != "t1" {
				t.Fatalf("access lookup tenant = %q, want t1", tenantID)
			}
			return &quartermasterpb.ClustersAccessResponse{
				Clusters: []*quartermasterpb.ClusterAccessEntry{
					{ClusterId: "c1", ClusterName: "Shared", AccessLevel: "shared"},
				},
			}, nil
		},
		// c1 is also owned -> its access level is upgraded to "owner"; c2 is owner-only.
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{
					{ClusterId: "c1", ClusterName: "Shared"},
					{ClusterId: "c2", ClusterName: "Owned"},
				},
			}, nil
		},
	}
	got, err := qmR(qm).DoGetClustersAccess(clientstest.AuthedCtx("t1"), nil, nil)
	if err != nil {
		t.Fatalf("DoGetClustersAccess: %v", err)
	}
	byID := map[string]*model.ClusterAccess{}
	for _, c := range got {
		byID[c.ClusterID] = c
	}
	if c1 := byID["c1"]; c1 == nil || c1.AccessLevel != "owner" {
		t.Fatalf("c1 access = %+v, want owner-upgraded", c1)
	}
	if c2 := byID["c2"]; c2 == nil || c2.AccessLevel != "owner" || c2.ClusterName != "Owned" {
		t.Fatalf("c2 access = %+v, want owner-only entry", c2)
	}

	// No tenant: guard short-circuits before any backend call.
	noTenant := &clientstest.FakeQuartermaster{}
	if _, err := qmR(noTenant).DoGetClustersAccess(context.Background(), nil, nil); err == nil {
		t.Fatal("expected tenant-required error")
	}
	if noTenant.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", noTenant.Calls)
	}
}

// ---- DoCreateEnrollmentToken: typed result; auth gate returns AuthError ----

func TestDoCreateEnrollmentToken_HappyAndAuthGate(t *testing.T) {
	var gotReq *quartermasterpb.CreateEnrollmentTokenRequest
	qm := &clientstest.FakeQuartermaster{
		CreateEnrollmentTokenFn: func(_ context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			gotReq = req
			return &quartermasterpb.CreateBootstrapTokenResponse{
				Token: &quartermasterpb.BootstrapToken{Id: "bt1", Token: "secret", Kind: "edge_node"},
			}, nil
		},
	}
	name, ttl := "ci-token", "24h"
	res, err := qmR(qm).DoCreateEnrollmentToken(qmUserCtx("t1"), "c1", &name, &ttl)
	if err != nil {
		t.Fatalf("DoCreateEnrollmentToken: %v", err)
	}
	if gotReq.GetClusterId() != "c1" || gotReq.GetTenantId() != "t1" || gotReq.GetName() != "ci-token" || gotReq.GetTtl() != "24h" {
		t.Fatalf("req = %+v, want cluster c1 / tenant t1 / name ci-token / ttl 24h", gotReq)
	}
	resp, ok := res.(*model.CreateEnrollmentTokenResponse)
	if !ok {
		t.Fatalf("result type = %T, want *model.CreateEnrollmentTokenResponse", res)
	}
	if resp.BootstrapToken.GetToken() != "secret" {
		t.Fatalf("token = %q, want secret", resp.BootstrapToken.GetToken())
	}

	// No user-derived tenant: returns a typed AuthError, not a Go error, and no backend call.
	guard := &clientstest.FakeQuartermaster{}
	gres, gerr := qmR(guard).DoCreateEnrollmentToken(clientstest.AuthedCtx("t1"), "c1", nil, nil)
	if gerr != nil {
		t.Fatalf("auth gate should return typed result, got err: %v", gerr)
	}
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result type = %T, want *model.AuthError", gres)
	}
	if guard.Calls != 0 {
		t.Fatalf("auth gate leaked a backend call: Calls=%d", guard.Calls)
	}

	// Backend error is classified into a ValidationError result (no Go error).
	failing := &clientstest.FakeQuartermaster{
		CreateEnrollmentTokenFn: func(context.Context, *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
			return nil, errors.New("boom")
		},
	}
	fres, ferr := qmR(failing).DoCreateEnrollmentToken(qmUserCtx("t1"), "c1", nil, nil)
	if ferr != nil {
		t.Fatalf("backend error should be classified, got err: %v", ferr)
	}
	if _, ok := fres.(*model.ValidationError); !ok {
		t.Fatalf("result type = %T, want *model.ValidationError", fres)
	}
}

// ---- DoListMarketplaceClusters: empty QM result returns early (before Purser) ----

func TestDoListMarketplaceClusters_EmptyAndError(t *testing.T) {
	var gotReq *quartermasterpb.ListMarketplaceClustersRequest
	// Empty cluster list short-circuits before the Purser pricing call, so a
	// nil Purser is safe here — this pins the "no Purser dependency on empty" path.
	qm := &clientstest.FakeQuartermaster{
		ListMarketplaceClustersFn: func(_ context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error) {
			gotReq = req
			return &quartermasterpb.ListMarketplaceClustersResponse{}, nil
		},
	}
	first := 4
	got, err := qmR(qm).DoListMarketplaceClusters(qmUserCtx("t1"), &first, nil)
	if err != nil {
		t.Fatalf("DoListMarketplaceClusters: %v", err)
	}
	if gotReq.GetTenantId() != "t1" {
		t.Fatalf("req tenant = %q, want t1", gotReq.GetTenantId())
	}
	if gotReq.GetPagination().GetFirst() != int32(first) {
		t.Fatalf("req first = %d, want %d", gotReq.GetPagination().GetFirst(), first)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty marketplace, got %d", len(got))
	}

	failing := &clientstest.FakeQuartermaster{
		ListMarketplaceClustersFn: func(context.Context, *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmR(failing).DoListMarketplaceClusters(qmUserCtx("t1"), nil, nil); err == nil {
		t.Fatal("expected error to propagate")
	}
}
