package middleware

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/auth"
	"frameworks/pkg/clients/commodore"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
)

func TestAuthenticateRequestNilRequest(t *testing.T) {
	_, err := AuthenticateRequest(context.Background(), nil, &clients.ServiceClients{}, []byte("secret"), AuthOptions{}, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestAuthenticateRequestWalletMissingHeaders(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-Wallet-Address", "0xabc")

	_, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{AllowWallet: true}, nil)
	if err == nil {
		t.Fatal("expected error for missing wallet headers")
	}
}

func TestAuthenticateRequestInvalidX402Header(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-PAYMENT", "not-base64")

	_, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{AllowX402: true}, nil)
	if err == nil {
		t.Fatal("expected error for invalid X-PAYMENT header")
	}
}

func TestAuthenticateRequestNoAuth(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, []byte("secret"), AuthOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for no auth, got %#v", result)
	}
}

func TestAuthenticateRequestJWT(t *testing.T) {
	secret := []byte("secret")
	token, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "admin", secret)
	if err != nil {
		t.Fatalf("failed to generate JWT: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{}, secret, AuthOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected auth result")
	}
	if result.AuthType != "jwt" {
		t.Fatalf("expected auth type jwt, got %q", result.AuthType)
	}
	if result.TenantID != "tenant-1" || result.UserID != "user-1" {
		t.Fatalf("unexpected tenant/user: %s/%s", result.TenantID, result.UserID)
	}
}

func TestAuthenticateRequestAPIToken(t *testing.T) {
	server := newFakeInternalService(&pb.ValidateAPITokenResponse{
		Valid:       true,
		UserId:      "user-2",
		TenantId:    "tenant-2",
		Email:       "api@example.com",
		Role:        "developer",
		TokenId:     "token-id",
		Permissions: []string{"streams:read"},
	}, nil)
	addr, cleanup := startInternalService(t, server)
	defer cleanup()

	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr: addr,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create commodore client: %v", err)
	}
	defer func() {
		_ = commodoreClient.Close()
	}()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer api-token")

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{Commodore: commodoreClient}, []byte("secret"), AuthOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected auth result")
	}
	if result.AuthType != "api_token" {
		t.Fatalf("expected auth type api_token, got %q", result.AuthType)
	}
	if result.TenantID != "tenant-2" || result.UserID != "user-2" {
		t.Fatalf("unexpected tenant/user: %s/%s", result.TenantID, result.UserID)
	}
	if len(result.Permissions) != 1 || result.Permissions[0] != "streams:read" {
		t.Fatalf("unexpected permissions: %#v", result.Permissions)
	}
}

type fakeInternalService struct {
	pb.UnimplementedInternalServiceServer
	validateResponse *pb.ValidateAPITokenResponse
	validateError    error
}

func newFakeInternalService(resp *pb.ValidateAPITokenResponse, err error) *fakeInternalService {
	return &fakeInternalService{
		validateResponse: resp,
		validateError:    err,
	}
}

func (f *fakeInternalService) ValidateAPIToken(ctx context.Context, _ *pb.ValidateAPITokenRequest) (*pb.ValidateAPITokenResponse, error) {
	return f.validateResponse, f.validateError
}

func startInternalService(t *testing.T, server pb.InternalServiceServer) (string, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterInternalServiceServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	return listener.Addr().String(), func() {
		grpcServer.Stop()
		_ = listener.Close()
	}
}
