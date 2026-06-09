package loaders

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func qmClients(f *clientstest.FakeQuartermaster) *clients.ServiceClients {
	return clientstest.Clients(clientstest.WithQuartermaster(f))
}

func TestNodeLoader_Caches(t *testing.T) {
	fake := &clientstest.FakeQuartermaster{
		GetNodeFn: func(_ context.Context, id string) (*quartermasterpb.NodeResponse, error) {
			return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{NodeId: id}}, nil
		},
	}
	l := NewNodeLoader(qmClients(fake))
	for range 3 {
		if _, err := l.Load(context.Background(), "n1"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Calls != 1 {
		t.Fatalf("GetNode called %d times, want 1", fake.Calls)
	}
}

func TestNodeLoader_PropagatesError(t *testing.T) {
	sentinel := errors.New("qm down")
	fake := &clientstest.FakeQuartermaster{
		GetNodeFn: func(context.Context, string) (*quartermasterpb.NodeResponse, error) { return nil, sentinel },
	}
	l := NewNodeLoader(qmClients(fake))
	if _, err := l.Load(context.Background(), "n1"); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}

func TestClusterLoader_Caches(t *testing.T) {
	fake := &clientstest.FakeQuartermaster{
		GetClusterFn: func(_ context.Context, id string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{}}, nil
		},
	}
	l := NewClusterLoader(qmClients(fake))
	for range 2 {
		if _, err := l.Load(context.Background(), "c1"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Calls != 1 {
		t.Fatalf("GetCluster called %d times, want 1", fake.Calls)
	}
}

func TestNodesByClusterLoader_Caches(t *testing.T) {
	fake := &clientstest.FakeQuartermaster{
		ListNodesFn: func(_ context.Context, clusterID, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			return &quartermasterpb.ListNodesResponse{Nodes: []*quartermasterpb.InfrastructureNode{{NodeId: "n1"}}}, nil
		},
	}
	l := NewNodesByClusterLoader(qmClients(fake))
	for range 2 {
		nodes, err := l.Load(context.Background(), "c1")
		if err != nil || len(nodes) != 1 {
			t.Fatalf("Load → (%v,%v)", nodes, err)
		}
	}
	if fake.Calls != 1 {
		t.Fatalf("ListNodes called %d times, want 1", fake.Calls)
	}
}

func TestServiceInstancesLoaders_CacheByClusterAndNode(t *testing.T) {
	fake := &clientstest.FakeQuartermaster{
		ListServiceInstancesFn: func(_ context.Context, clusterID, _, nodeID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
			return &quartermasterpb.ListServiceInstancesResponse{Instances: []*quartermasterpb.ServiceInstance{{}}}, nil
		},
	}
	sc := qmClients(fake)
	byCluster := NewServiceInstancesByClusterLoader(sc)
	byNode := NewServiceInstancesByNodeLoader(sc)

	for range 2 {
		if _, err := byCluster.Load(context.Background(), "c1"); err != nil {
			t.Fatal(err)
		}
		if _, err := byNode.Load(context.Background(), "n1"); err != nil {
			t.Fatal(err)
		}
	}
	// One call per distinct loader/key; repeats are cache-served.
	if fake.Calls != 2 {
		t.Fatalf("ListServiceInstances called %d times, want 2", fake.Calls)
	}
}

func TestLiveNodeStateLoader_LoadsOnceAndRequiresTenant(t *testing.T) {
	fake := &clientstest.FakePeriscope{
		GetLiveNodesFn: func(_ context.Context, _ string, _ *string, _ []string) (*periscopepb.GetLiveNodesResponse, error) {
			return &periscopepb.GetLiveNodesResponse{Nodes: []*periscopepb.LiveNode{{NodeId: "n1"}}}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		ListMySubscriptionsFn: func(context.Context, *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPeriscope(fake), clientstest.WithQuartermaster(qm))
	l := NewLiveNodeStateLoader(sc)

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "t1")
	for range 3 {
		n, err := l.Load(ctx, "n1")
		if err != nil || n == nil || n.NodeId != "n1" {
			t.Fatalf("Load → (%v,%v)", n, err)
		}
	}
	// once.Do guarantees a single batch fetch regardless of per-node lookups.
	if fake.Calls != 1 {
		t.Fatalf("GetLiveNodes called %d times, want 1 (once.Do)", fake.Calls)
	}
	// Unknown node is a cache miss returning nil, no extra fetch.
	if n, err := l.Load(ctx, "ghost"); err != nil || n != nil {
		t.Fatalf("unknown node → (%v,%v), want (nil,nil)", n, err)
	}
}

func TestLiveNodeStateLoader_MissingTenantErrors(t *testing.T) {
	// No tenant in context → guarded failure before any backend call.
	l := NewLiveNodeStateLoader(clientstest.Clients())
	if _, err := l.Load(context.Background(), "n1"); err == nil {
		t.Fatal("want error when tenant context is absent")
	}
}
