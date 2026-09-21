package bosun

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

type recordingServer struct {
	bosunpb.UnimplementedBosunServiceServer
	md       metadata.MD
	deadline time.Duration
	replayed string
	rotated  *bosunpb.RotateWebhookEndpointSecretRequest
}

func (s *recordingServer) record(ctx context.Context) {
	s.md, _ = metadata.FromIncomingContext(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = time.Until(deadline)
	}
}

func (s *recordingServer) GetWebhookEndpoint(ctx context.Context, req *bosunpb.GetWebhookEndpointRequest) (*bosunpb.WebhookEndpoint, error) {
	s.record(ctx)
	return &bosunpb.WebhookEndpoint{Id: req.GetEndpointId()}, nil
}

func (s *recordingServer) TestWebhookEndpoint(ctx context.Context, req *bosunpb.TestWebhookEndpointRequest) (*bosunpb.TestWebhookEndpointResponse, error) {
	s.record(ctx)
	return &bosunpb.TestWebhookEndpointResponse{Delivery: &bosunpb.WebhookDelivery{EndpointId: req.GetEndpointId()}}, nil
}

func (s *recordingServer) ReplayWebhookDelivery(ctx context.Context, req *bosunpb.ReplayWebhookDeliveryRequest) (*bosunpb.WebhookDelivery, error) {
	s.record(ctx)
	s.replayed = req.GetDeliveryId()
	return &bosunpb.WebhookDelivery{Id: req.GetDeliveryId()}, nil
}

func (s *recordingServer) RotateWebhookEndpointSecret(ctx context.Context, req *bosunpb.RotateWebhookEndpointSecretRequest) (*bosunpb.WebhookEndpointWithSecret, error) {
	s.record(ctx)
	s.rotated = req
	return &bosunpb.WebhookEndpointWithSecret{Endpoint: &bosunpb.WebhookEndpoint{Id: req.GetEndpointId()}, Secret: "whsec_x"}, nil
}

func startClient(t *testing.T) (*GRPCClient, *recordingServer) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	srv := &recordingServer{}
	server := grpc.NewServer()
	bosunpb.RegisterBosunServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(authInterceptor("service-secret")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := newWithConn(conn, nil, 5*time.Second)
	t.Cleanup(func() { _ = client.Close() })
	return client, srv
}

func TestCallsForwardTheCallerJWTAndTenant(t *testing.T) {
	client, srv := startClient(t)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-tenant-id", "forged"))
	got, err := client.GetWebhookEndpoint(ctx, "ep-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetId() != "ep-1" {
		t.Fatalf("endpoint = %v", got)
	}
	if v := srv.md.Get("authorization"); len(v) != 1 || v[0] != "Bearer caller-jwt" {
		t.Fatalf("authorization = %v", v)
	}
	if v := srv.md.Get("x-tenant-id"); len(v) != 1 || v[0] != "tenant-1" {
		t.Fatalf("x-tenant-id = %v, want the caller's tenant only", v)
	}
}

func TestCallsWithoutJWTUseTheServiceToken(t *testing.T) {
	client, srv := startClient(t)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-2")
	if _, err := client.ReplayWebhookDelivery(ctx, "d-1"); err != nil {
		t.Fatal(err)
	}
	if v := srv.md.Get("authorization"); len(v) != 1 || v[0] != "Bearer service-secret" {
		t.Fatalf("authorization = %v", v)
	}
	if srv.replayed != "d-1" {
		t.Fatalf("replayed = %q", srv.replayed)
	}
	if _, err := client.RotateWebhookEndpointSecret(ctx, "ep-2", true); err != nil {
		t.Fatal(err)
	}
	if srv.rotated.GetEndpointId() != "ep-2" || !srv.rotated.GetRevokePrevious() {
		t.Fatalf("rotate request = %v", srv.rotated)
	}
}

func TestTestDeliveryGetsTheLongerDeadline(t *testing.T) {
	client, srv := startClient(t)
	if _, err := client.TestWebhookEndpoint(context.Background(), "ep-1"); err != nil {
		t.Fatal(err)
	}
	if srv.deadline < 15*time.Second {
		t.Fatalf("test call deadline = %s, want the test timeout above the 10 s attempt", srv.deadline)
	}
	if _, err := client.GetWebhookEndpoint(context.Background(), "ep-1"); err != nil {
		t.Fatal(err)
	}
	if srv.deadline > 6*time.Second {
		t.Fatalf("ordinary call deadline = %s, want the client timeout", srv.deadline)
	}
}
