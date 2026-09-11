package main

import (
	"context"
	"net"
	"testing"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type placementInventoryServer struct {
	quartermasterpb.UnimplementedClusterServiceServer
	nodeID string
}

func (server placementInventoryServer) GetMediaPlacementInventory(_ context.Context, request *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	return &quartermasterpb.MediaPlacementInventory{TenantId: request.TenantId, ControlCellId: request.ControlCellId, ClusterIds: request.ClusterIds,
		Complete: true, Nodes: []*quartermasterpb.MediaPlacementInventoryNode{{NodeId: server.nodeID}}}, nil
}

func TestLivePlacementInventoryFollowsConnectedClient(t *testing.T) {
	previous := releaseReconcilerClient.Swap(nil)
	t.Cleanup(func() { releaseReconcilerClient.Store(previous) })
	reader := livePlacementInventory{}
	request := &quartermasterpb.GetMediaPlacementInventoryRequest{TenantId: "tenant", ControlCellId: "cell", ClusterIds: []string{"cluster"}}
	for _, nodeID := range []string{"first-client", "replacement-client"} {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer()
		quartermasterpb.RegisterClusterServiceServer(server, placementInventoryServer{nodeID: nodeID})
		t.Cleanup(server.Stop)
		go func() { _ = server.Serve(listener) }()
		client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		releaseReconcilerClient.Store(client)
		response, lookupErr := reader.GetMediaPlacementInventory(context.Background(), request)
		if lookupErr != nil || len(response.GetNodes()) != 1 || response.Nodes[0].NodeId != nodeID || response.TenantId != request.TenantId || response.ControlCellId != request.ControlCellId || len(response.ClusterIds) != 1 || response.ClusterIds[0] != "cluster" {
			t.Fatalf("inventory did not follow current client with exact scope: %v, %v", response, lookupErr)
		}
	}
	releaseReconcilerClient.Store(nil)
	if response, err := reader.GetMediaPlacementInventory(context.Background(), request); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("disconnected inventory reused previous client: %v, %v", response, err)
	}
}

func TestLivePlacementInventoryWithoutConnectedClient(t *testing.T) {
	previous := releaseReconcilerClient.Swap(nil)
	t.Cleanup(func() { releaseReconcilerClient.Store(previous) })
	reader := livePlacementInventory{}
	request := &quartermasterpb.GetMediaPlacementInventoryRequest{TenantId: "tenant", ControlCellId: "cell"}
	response, err := reader.GetMediaPlacementInventory(context.Background(), request)
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("degraded bootstrap claimed inventory: %v, %v", response, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = reader.GetMediaPlacementInventory(ctx, request)
	if response != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("canceled inventory request: %v, %v", response, err)
	}
}
