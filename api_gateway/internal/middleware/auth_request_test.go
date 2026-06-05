package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	server := newFakeInternalService(&commodorepb.ValidateAPITokenResponse{
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
		GRPCAddr:      addr,
		Timeout:       5 * time.Second,
		AllowInsecure: true,
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

func TestAuthenticateRequestWalletSuccess(t *testing.T) {
	server := newFakeCommodoreService(nil, &commodorepb.AuthResponse{
		Token: "wallet-token",
		User: &commodorepb.User{
			Id:       "user-wallet",
			TenantId: "tenant-wallet",
			Role:     "viewer",
		},
	})
	addr, cleanup := startCommodoreService(t, server, server)
	defer cleanup()

	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      addr,
		Timeout:       5 * time.Second,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("failed to create commodore client: %v", err)
	}
	defer func() {
		_ = commodoreClient.Close()
	}()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-Wallet-Address", "0xabc")
	req.Header.Set("X-Wallet-Message", "message")
	req.Header.Set("X-Wallet-Signature", "signature")

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{Commodore: commodoreClient}, []byte("secret"), AuthOptions{AllowWallet: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected auth result")
	}
	if result.AuthType != "wallet" {
		t.Fatalf("expected auth type wallet, got %q", result.AuthType)
	}
	if result.UserID != "user-wallet" || result.TenantID != "tenant-wallet" {
		t.Fatalf("unexpected tenant/user: %s/%s", result.TenantID, result.UserID)
	}
	if result.WalletAddress != "0xabc" {
		t.Fatalf("unexpected wallet address: %s", result.WalletAddress)
	}
}

func TestAuthenticateRequestX402Success(t *testing.T) {
	expiresAt := time.Now().Add(10 * time.Minute)
	server := newFakeCommodoreService(&commodorepb.WalletLoginWithX402Response{
		Auth: &commodorepb.AuthResponse{
			Token: "x402-token",
			User: &commodorepb.User{
				Id:       "user-x402",
				TenantId: "tenant-x402",
				Role:     "viewer",
			},
			ExpiresAt: timestamppb.New(expiresAt),
		},
		IsAuthOnly:   true,
		PayerAddress: "",
	}, nil)
	addr, cleanup := startCommodoreService(t, server, server)
	defer cleanup()

	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:      addr,
		Timeout:       5 * time.Second,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("failed to create commodore client: %v", err)
	}
	defer func() {
		_ = commodoreClient.Close()
	}()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("X-PAYMENT", buildX402PaymentHeader(t, "0xabc"))
	req.RemoteAddr = "203.0.113.9:1234"

	result, err := AuthenticateRequest(context.Background(), req, &clients.ServiceClients{Commodore: commodoreClient}, []byte("secret"), AuthOptions{AllowX402: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected auth result")
	}
	if result.AuthType != "x402" {
		t.Fatalf("expected auth type x402, got %q", result.AuthType)
	}
	if result.JWTToken != "x402-token" || result.UserID != "user-x402" {
		t.Fatalf("unexpected auth result: %#v", result)
	}
	if result.WalletAddress != "0xabc" {
		t.Fatalf("expected fallback wallet address, got %q", result.WalletAddress)
	}
	if result.ExpiresAt == nil || !result.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected expiresAt: %#v", result.ExpiresAt)
	}
	if !result.X402AuthOnly {
		t.Fatal("expected x402 auth-only result")
	}
}

func TestApplyAuthToContextAPIToken(t *testing.T) {
	ctx := ApplyAuthToContext(context.Background(), &AuthResult{
		UserID:      "user-1",
		TenantID:    "tenant-1",
		Email:       "user@example.com",
		Role:        "admin",
		AuthType:    "api_token",
		APIToken:    "api-token",
		TokenID:     "token-id",
		Permissions: []string{"streams:read"},
	})

	if ctx.Value(ctxkeys.KeyAPITokenHash) == nil {
		t.Fatal("expected api token hash in context")
	}
	perms, ok := ctx.Value(ctxkeys.KeyPermissions).([]string)
	if !ok || len(perms) != 1 || perms[0] != "streams:read" {
		t.Fatalf("unexpected permissions: %#v", ctx.Value(ctxkeys.KeyPermissions))
	}
}

func TestApplyAuthToContextX402(t *testing.T) {
	expiresAt := time.Now().Add(5 * time.Minute)
	ctx := ApplyAuthToContext(context.Background(), &AuthResult{
		UserID:        "user-1",
		TenantID:      "tenant-1",
		AuthType:      "x402",
		JWTToken:      "token",
		ExpiresAt:     &expiresAt,
		X402Processed: true,
		X402AuthOnly:  true,
	})

	if ctx.Value(ctxkeys.KeyX402Processed) != true {
		t.Fatal("expected x402 processed flag")
	}
	if ctx.Value(ctxkeys.KeyX402AuthOnly) != true {
		t.Fatal("expected x402 auth-only flag")
	}
	if ctx.Value(ctxkeys.KeySessionToken) != "token" {
		t.Fatalf("expected session token in context, got %#v", ctx.Value(ctxkeys.KeySessionToken))
	}
	if ctx.Value(ctxkeys.KeyJWTExpiresAt) == nil {
		t.Fatal("expected expires at in context")
	}
}

type fakeInternalService struct {
	commodorepb.UnimplementedInternalServiceServer
	validateResponse *commodorepb.ValidateAPITokenResponse
	validateError    error
}

func newFakeInternalService(resp *commodorepb.ValidateAPITokenResponse, err error) *fakeInternalService {
	return &fakeInternalService{
		validateResponse: resp,
		validateError:    err,
	}
}

func (f *fakeInternalService) ValidateAPIToken(ctx context.Context, _ *commodorepb.ValidateAPITokenRequest) (*commodorepb.ValidateAPITokenResponse, error) {
	return f.validateResponse, f.validateError
}

func startInternalService(t *testing.T, server commodorepb.InternalServiceServer) (string, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	return listener.Addr().String(), func() {
		grpcServer.Stop()
		_ = listener.Close()
	}
}

type fakeCommodoreService struct {
	commodorepb.UnimplementedInternalServiceServer
	commodorepb.UnimplementedUserServiceServer
	x402Response *commodorepb.WalletLoginWithX402Response
	x402Error    error
	walletResp   *commodorepb.AuthResponse
	walletErr    error
}

func newFakeCommodoreService(x402Resp *commodorepb.WalletLoginWithX402Response, walletResp *commodorepb.AuthResponse) *fakeCommodoreService {
	return &fakeCommodoreService{
		x402Response: x402Resp,
		walletResp:   walletResp,
	}
}

func (f *fakeCommodoreService) WalletLoginWithX402(ctx context.Context, _ *commodorepb.WalletLoginWithX402Request) (*commodorepb.WalletLoginWithX402Response, error) {
	return f.x402Response, f.x402Error
}

func (f *fakeCommodoreService) WalletLogin(ctx context.Context, _ *commodorepb.WalletLoginRequest) (*commodorepb.AuthResponse, error) {
	return f.walletResp, f.walletErr
}

func startCommodoreService(t *testing.T, internalServer commodorepb.InternalServiceServer, userServer commodorepb.UserServiceServer) (string, func()) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	if internalServer != nil {
		commodorepb.RegisterInternalServiceServer(grpcServer, internalServer)
	}
	if userServer != nil {
		commodorepb.RegisterUserServiceServer(grpcServer, userServer)
	}

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	return listener.Addr().String(), func() {
		grpcServer.Stop()
		_ = listener.Close()
	}
}

func buildX402PaymentHeader(t *testing.T, from string) string {
	t.Helper()

	payload := map[string]any{
		"x402Version": 1,
		"scheme":      "x402",
		"network":     "base",
		"payload": map[string]any{
			"signature": "sig",
			"authorization": map[string]any{
				"from":        from,
				"to":          "0xreceiver",
				"value":       "100",
				"validAfter":  "0",
				"validBefore": "999999",
				"nonce":       "1",
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
