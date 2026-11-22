package control

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

type processingInventoryServer struct {
	quartermasterpb.UnimplementedNodeServiceServer
	repeatCursor atomic.Bool
	delay        atomic.Int64
}

func (s *processingInventoryServer) ListNodes(ctx context.Context, request *quartermasterpb.ListNodesRequest) (*quartermasterpb.ListNodesResponse, error) {
	if delay := time.Duration(s.delay.Load()); delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	response := &quartermasterpb.ListNodesResponse{}
	if request.GetClusterId() != "origin" || request.GetNodeType() != "edge" {
		return response, nil
	}
	cursor := "next"
	if request.GetPagination().GetAfter() == "" || s.repeatCursor.Load() {
		response.Nodes = []*quartermasterpb.InfrastructureNode{{NodeId: "small", ClusterId: "origin"}}
		response.Pagination = &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: &cursor}
	} else {
		// No heartbeat: registration, not current liveness, is authoritative here.
		response.Nodes = []*quartermasterpb.InfrastructureNode{{NodeId: "absent-large", ClusterId: "origin"}, {NodeId: "foreign", ClusterId: "other"}, {NodeId: "retired", ClusterId: "origin", Status: "retired"}, {NodeId: "evicted", ClusterId: "origin", Status: "evicted"}}
	}
	return response, nil
}

func TestRegisteredProcessingNodesIncludesAbsentPaginatedEdge(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &processingInventoryServer{}
	server := grpc.NewServer()
	quartermasterpb.RegisterNodeServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	previous := quartermasterClient
	quartermasterClient = client
	t.Cleanup(func() { quartermasterClient = previous })
	ids, err := RegisteredProcessingNodes(context.Background(), "origin")
	if err != nil || len(ids) != 2 || ids[0] != "small" || ids[1] != "absent-large" {
		t.Fatalf("incomplete inventory: %v %v", ids, err)
	}
	// Use a fresh server configuration only after the previous RPCs completed.
	fake.repeatCursor.Store(true)
	if _, err = RegisteredProcessingNodes(context.Background(), "origin"); err == nil {
		t.Fatal("incomplete repeated page accepted as complete inventory")
	}
	quartermasterClient = nil
	if _, err = RegisteredProcessingNodes(context.Background(), "origin"); err == nil {
		t.Fatal("unavailable inventory accepted as an empty cluster")
	}
}

func TestProcessingInventoryHonorsDispatchDeadline(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &processingInventoryServer{}
	fake.delay.Store(int64(2 * time.Second))
	server := grpc.NewServer()
	quartermasterpb.RegisterNodeServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	previous := quartermasterClient
	quartermasterClient = client
	t.Cleanup(func() { quartermasterClient = previous })
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = RegisteredProcessingNodes(ctx, "origin")
	if err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("inventory lookup outlived dispatch deadline: duration=%s error=%v", time.Since(start), err)
	}
}
