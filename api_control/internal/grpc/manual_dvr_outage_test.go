package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestManualDVRBillingOutageReturnsBeforeCallerExpires(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	purserpb.RegisterBillingServiceServer(rpc, &purserEntitlementFake{
		billingStatus: func(context.Context, *purserpb.GetTenantBillingStatusRequest) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{DvrPolicy: manualDVRBillingPolicy()}, nil
		},
	})
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	client, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(), AllowInsecure: true, Timeout: 5 * time.Second,
		ServiceToken: "commodore-service-token", PreferServiceToken: true, Logger: logging.NewLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, warmErr := client.GetTenantBillingStatus(t.Context(), "recording-tenant"); warmErr != nil {
		t.Fatalf("establish healthy billing connection: %v", warmErr)
	}
	rpc.Stop()
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(manualDVRContext(), 3*time.Second)
	defer cancel()
	s := &CommodoreServer{purserClient: client, logger: logging.NewLogger()}
	response, err := s.StartDVR(ctx, &sharedpb.StartDVRRequest{
		InternalName: "recording-source", StreamId: proto.String("recording-stream"),
	})
	if response != nil || status.Code(err) != codes.Unavailable || ctx.Err() != nil {
		t.Fatalf("manual start waited for billing reconnection instead of returning policy unavailable: response=%v err=%v caller=%v", response, err, ctx.Err())
	}
}
