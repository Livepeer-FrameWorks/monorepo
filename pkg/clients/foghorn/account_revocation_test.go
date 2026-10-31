package foghorn

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tenantWideOnlyServer struct {
	foghornpb.UnimplementedTenantControlServiceServer
	calls atomic.Int32
}

func (s *tenantWideOnlyServer) TerminateTenantStreams(context.Context, *controlpb.TerminateTenantStreamsRequest) (*controlpb.TerminateTenantStreamsResponse, error) {
	s.calls.Add(1)
	return &controlpb.TerminateTenantStreamsResponse{StreamsTerminated: 2}, nil
}

func TestOwnerRevocationDoesNotFallBackToTenantWideRPC(t *testing.T) {
	listener, listenErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	server := grpc.NewServer()
	oldCell := &tenantWideOnlyServer{}
	foghornpb.RegisterTenantControlServiceServer(server, oldCell)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	client, clientErr := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger()})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	defer client.Close()
	_, _, stopErr := client.TerminateNamedTenantStreams(t.Context(), "tenant", "account_inactive", []string{"one-owner"})
	if status.Code(stopErr) != codes.Unimplemented || oldCell.calls.Load() != 0 {
		t.Fatalf("owner revocation reached old tenant-wide stop: error=%v tenant-wide calls=%d", stopErr, oldCell.calls.Load())
	}
}
