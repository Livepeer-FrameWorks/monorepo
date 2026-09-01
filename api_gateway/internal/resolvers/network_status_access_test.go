package resolvers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func networkStatusAccessResolver(public, subscribed, owned []*quartermasterpb.InfrastructureCluster) *Resolver {
	nodeID := "edge-1"
	now := timestamppb.New(time.Now())
	qm := &clientstest.FakeQuartermaster{
		ListPublicTopologyClustersFn: func(context.Context) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: public}, nil
		},
		ListMySubscriptionsFn: func(context.Context, *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: subscribed}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: owned}, nil
		},
		ListNodesFn: func(_ context.Context, clusterID, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
			return &quartermasterpb.ListNodesResponse{Nodes: []*quartermasterpb.InfrastructureNode{{
				NodeId: nodeID, ClusterId: clusterID, NodeName: "Private edge", NodeType: "media",
				LastHeartbeat: now,
			}}}, nil
		},
		ListServiceInstancesFn: func(_ context.Context, clusterID, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
			return &quartermasterpb.ListServiceInstancesResponse{Instances: []*quartermasterpb.ServiceInstance{{
				Id: "service-row-1", InstanceId: "mist-1", ServiceId: "mistserver", ClusterId: clusterID,
				NodeId: &nodeID, Status: "running", HealthStatus: "healthy",
			}}}, nil
		},
		GetServicePoolStatusFn: func(context.Context, string) (*quartermasterpb.GetServicePoolStatusResponse, error) {
			return &quartermasterpb.GetServicePoolStatusResponse{}, nil
		},
	}
	periscope := &clientstest.FakePeriscope{
		GetNetworkLiveStatsFn: func(context.Context) (*periscopepb.GetNetworkLiveStatsResponse, error) {
			return &periscopepb.GetNetworkLiveStatsResponse{Clusters: []*periscopepb.NetworkClusterLiveStats{{
				ClusterId: "cluster-a", ActiveStreams: 4, CurrentViewers: 12, ActiveNodes: 1,
			}}}, nil
		},
	}
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithQuartermaster(qm), clientstest.WithPeriscope(periscope)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func networkStatusTestCluster(owner string) *quartermasterpb.InfrastructureCluster {
	return &quartermasterpb.InfrastructureCluster{
		Id: "cluster-row-a", ClusterId: "cluster-a", ClusterName: "Cluster A", ClusterType: "edge",
		OwnerTenantId: &owner, IsActive: true, HealthStatus: "healthy",
	}
}

func TestNetworkStatusAnonymousPublicTopologyIncludesPublishedNodesOnly(t *testing.T) {
	cluster := networkStatusTestCluster("platform")
	got, err := networkStatusAccessResolver([]*quartermasterpb.InfrastructureCluster{cluster}, nil, nil).DoGetNetworkStatus(context.Background())
	if err != nil {
		t.Fatalf("DoGetNetworkStatus: %v", err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].CurrentViewers != 12 || got.Clusters[0].NodeCount != 1 {
		t.Fatalf("public aggregate missing: %+v", got.Clusters)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].NodeID != "edge-1" {
		t.Fatalf("published public node missing: %+v", got.Nodes)
	}
	if len(got.ServiceInstances) != 0 {
		t.Fatalf("public topology leaked service instances: %+v", got.ServiceInstances)
	}
}

func TestNetworkStatusSubscriberGetsAggregateWithoutPrivateInventory(t *testing.T) {
	cluster := networkStatusTestCluster("provider-tenant")
	got, err := networkStatusAccessResolver(nil, []*quartermasterpb.InfrastructureCluster{cluster}, nil).DoGetNetworkStatus(clientstest.AuthedCtx("subscriber-tenant"))
	if err != nil {
		t.Fatalf("DoGetNetworkStatus: %v", err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].CurrentStreams != 4 || got.Clusters[0].CurrentViewers != 12 || got.Clusters[0].NodeCount != 1 {
		t.Fatalf("subscriber aggregate missing: %+v", got.Clusters)
	}
	if len(got.Nodes) != 0 || len(got.ServiceInstances) != 0 {
		t.Fatalf("subscriber received private inventory: nodes=%+v services=%+v", got.Nodes, got.ServiceInstances)
	}
}

func TestNetworkStatusOwnerGetsPrivateNodeAndServiceInventory(t *testing.T) {
	cluster := networkStatusTestCluster("owner-tenant")
	got, err := networkStatusAccessResolver(nil, nil, []*quartermasterpb.InfrastructureCluster{cluster}).DoGetNetworkStatus(clientstest.AuthedCtx("owner-tenant"))
	if err != nil {
		t.Fatalf("DoGetNetworkStatus: %v", err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].NodeID != "edge-1" {
		t.Fatalf("owner node inventory missing: %+v", got.Nodes)
	}
	if len(got.ServiceInstances) != 1 || got.ServiceInstances[0].InstanceID != "mist-1" {
		t.Fatalf("owner service inventory missing: %+v", got.ServiceInstances)
	}
}
