package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func delegatedBillingContext(tenant string, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

func billingActorContext(authType, tenant, role string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	return context.WithValue(ctx, ctxkeys.KeyRole, role)
}

func TestBillingMutationAuthorizationRequiresOwnerOrAdmin(t *testing.T) {
	interceptor := billingMutationAuthorizationInterceptor()
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/ChangeBillingTier"}
	req := &purserpb.ChangeBillingTierRequest{TenantId: "tenant-1"}
	handler := func(context.Context, any) (any, error) { return "called", nil }

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{name: "owner", ctx: billingActorContext("jwt", "tenant-1", "owner"), want: codes.OK},
		{name: "admin", ctx: billingActorContext("wallet", "tenant-1", "admin"), want: codes.OK},
		{name: "member", ctx: billingActorContext("jwt", "tenant-1", "member"), want: codes.PermissionDenied},
		{name: "foreign owner", ctx: billingActorContext("jwt", "tenant-2", "owner"), want: codes.PermissionDenied},
		{name: "scoped api token without privileged role", ctx: delegatedBillingContext("tenant-1", "billing:write"), want: codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interceptor(tc.ctx, req, method, handler)
			if status.Code(err) != tc.want {
				t.Fatalf("status = %v, want %v: %v", status.Code(err), tc.want, err)
			}
		})
	}
}

func TestBillingMutationAuthorizationProtectsDirectBalanceRPCs(t *testing.T) {
	interceptor := billingMutationAuthorizationInterceptor()
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/AdjustBalance"}
	req := &purserpb.AdjustBalanceRequest{TenantId: "tenant-1"}
	for _, role := range []string{"member", "admin", "owner"} {
		t.Run(role, func(t *testing.T) {
			_, err := interceptor(billingActorContext("jwt", "tenant-1", role), req, method,
				func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil })
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
			}
		})
	}
	called := false
	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := interceptor(serviceCtx, req, method, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	}); err != nil || !called {
		t.Fatalf("service mutation = called %v, err %v", called, err)
	}
}

func TestBillingMutationAuthorizationProtectsEveryPrivilegedRPC(t *testing.T) {
	interceptor := billingMutationAuthorizationInterceptor()
	methods := []string{
		"/purser.PrepaidService/TopupBalance",
		"/purser.PrepaidService/DeductBalance",
		"/purser.PrepaidService/AdjustBalance",
		"/purser.BillingService/CreateBillingTier",
		"/purser.BillingService/UpdateBillingTier",
		"/purser.PrepaidService/InitializePrepaidBalance",
		"/purser.PrepaidService/InitializePrepaidAccount",
		"/purser.PrepaidService/InitializePostpaidAccount",
		"/purser.PrepaidService/EnsureFreeAccount",
		"/purser.StripeService/SyncSubscription",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			called := false
			handler := func(context.Context, any) (any, error) {
				called = true
				return nil, nil
			}
			_, err := interceptor(
				billingActorContext("jwt", "tenant-1", "owner"),
				nil,
				&grpcpkg.UnaryServerInfo{FullMethod: method},
				handler,
			)
			if status.Code(err) != codes.PermissionDenied || called {
				t.Fatalf("owner result = called %v, status %v; want denied before handler", called, status.Code(err))
			}

			serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
			if _, err := interceptor(serviceCtx, nil, &grpcpkg.UnaryServerInfo{FullMethod: method}, handler); err != nil || !called {
				t.Fatalf("service result = called %v, err %v; want allowed", called, err)
			}
		})
	}
}

func TestBillingMutationAuthorizationUsesAuthenticatedTenantForCreatePayment(t *testing.T) {
	interceptor := billingMutationAuthorizationInterceptor()
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.PaymentService/CreatePayment"}
	req := &purserpb.PaymentRequest{InvoiceId: "invoice-1", Method: "card"}
	handler := func(context.Context, any) (any, error) { return "called", nil }

	if _, err := interceptor(billingActorContext("jwt", "tenant-1", "owner"), req, method, handler); err != nil {
		t.Fatalf("owner payment denied: %v", err)
	}
	if _, err := interceptor(billingActorContext("jwt", "tenant-1", "member"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member payment status = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := interceptor(billingActorContext("jwt", "", "owner"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("tenantless payment status = %v, want PermissionDenied", status.Code(err))
	}
}

type paymentAuthorizationStub struct {
	purserpb.UnimplementedPaymentServiceServer
	calls int
}

func (s *paymentAuthorizationStub) CreatePayment(context.Context, *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
	s.calls++
	return &purserpb.PaymentResponse{}, nil
}

type clusterPricingAuthorizationStub struct {
	purserpb.UnimplementedClusterPricingServiceServer
	server *PurserServer
	calls  int
}

func (s *clusterPricingAuthorizationStub) SetClusterPricing(ctx context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	if err := s.server.authorizeClusterPricingMutation(ctx, req.GetClusterId()); err != nil {
		return nil, err
	}
	s.calls++
	return &purserpb.ClusterPricing{}, nil
}

func TestSetClusterPricingAuthorizationOverWire(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("cluster-pricing-wire-secret")
	ownerTenant := "tenant-1"
	authority := &PurserServer{quartermasterClient: &commercialQuartermasterStub{
		cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "cluster-1", OwnerTenantId: &ownerTenant},
	}}
	cfg := GRPCServerConfig{
		Logger: logging.NewLogger(), JWTSecret: secret, ServiceToken: "service-secret", AllowInsecure: true,
	}
	server := grpcpkg.NewServer(grpcpkg.ChainUnaryInterceptor(purserUnaryInterceptors(cfg)...))
	stub := &clusterPricingAuthorizationStub{server: authority}
	purserpb.RegisterClusterPricingServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpcpkg.NewClient(listener.Addr().String(), grpcpkg.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := purserpb.NewClusterPricingServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ownerJWT, err := auth.GenerateJWT("owner-user", ownerTenant, "owner@example.com", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	memberJWT, err := auth.GenerateJWT("member-user", ownerTenant, "member@example.com", "member", secret)
	if err != nil {
		t.Fatal(err)
	}
	foreignJWT, err := auth.GenerateJWT("foreign-user", "tenant-2", "foreign@example.com", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	ownerCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+ownerJWT))
	if _, err := client.SetClusterPricing(ownerCtx, &purserpb.SetClusterPricingRequest{ClusterId: "cluster-1"}); err != nil {
		t.Fatalf("owner pricing update denied: %v", err)
	}
	memberCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+memberJWT))
	if _, err := client.SetClusterPricing(memberCtx, &purserpb.SetClusterPricingRequest{ClusterId: "cluster-1"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member status = %v, want PermissionDenied", status.Code(err))
	}
	foreignCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+foreignJWT))
	if _, err := client.SetClusterPricing(foreignCtx, &purserpb.SetClusterPricingRequest{ClusterId: "cluster-1"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign owner status = %v, want PermissionDenied", status.Code(err))
	}
	if stub.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", stub.calls)
	}
}

func TestCreatePaymentAuthorizationOverWire(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	identityInterceptor := func(ctx context.Context, req any, info *grpcpkg.UnaryServerInfo, handler grpcpkg.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
		ctx = context.WithValue(ctx, ctxkeys.KeyRole, md.Get("test-role")[0])
		return handler(ctx, req)
	}
	server := grpcpkg.NewServer(grpcpkg.ChainUnaryInterceptor(
		identityInterceptor,
		apiTokenAuthorizationInterceptor(),
		billingMutationAuthorizationInterceptor(),
	))
	stub := &paymentAuthorizationStub{}
	purserpb.RegisterPaymentServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	conn, err := grpcpkg.NewClient(listener.Addr().String(), grpcpkg.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := purserpb.NewPaymentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("test-role", "owner"))
	if _, err := client.CreatePayment(ctx, &purserpb.PaymentRequest{InvoiceId: "invoice-1", Method: "card"}); err != nil {
		t.Fatalf("owner payment denied: %v", err)
	}
	memberCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("test-role", "member"))
	if _, err := client.CreatePayment(memberCtx, &purserpb.PaymentRequest{InvoiceId: "invoice-1", Method: "card"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member status = %v, want PermissionDenied", status.Code(err))
	}
	if stub.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", stub.calls)
	}
}

func TestBillingMutationAuthorizationProtectsAccountBootstrapRPCs(t *testing.T) {
	_, err := billingMutationAuthorizationInterceptor()(
		billingActorContext("jwt", "tenant-1", "owner"),
		&purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"},
		&grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/InitializePrepaidAccount"},
		func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil },
	)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestBillingMutationAuthorizationProtectsAdministrativeStripeSync(t *testing.T) {
	_, err := billingMutationAuthorizationInterceptor()(
		billingActorContext("jwt", "tenant-1", "owner"),
		&purserpb.SyncStripeSubscriptionRequest{TenantId: "tenant-1"},
		&grpcpkg.UnaryServerInfo{FullMethod: "/purser.StripeService/SyncSubscription"},
		func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil },
	)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestAPITokenAuthorizationEnforcesScopeAndTenant(t *testing.T) {
	interceptor := apiTokenAuthorizationInterceptor()
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/ChangeBillingTier"}
	req := &purserpb.ChangeBillingTierRequest{TenantId: "tenant-1"}
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }

	if _, err := interceptor(delegatedBillingContext("tenant-1", "billing:read"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read-only write error = %v", err)
	}
	if called {
		t.Fatal("handler called for read-only token")
	}
	if _, err := interceptor(delegatedBillingContext("tenant-2", "billing:write"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-tenant error = %v", err)
	}
	if _, err := interceptor(delegatedBillingContext("tenant-1", "billing:write"), req, method, handler); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler was not called for authorized tenant write")
	}
}

func TestAPITokenAuthorizationDeniesUnknownRPC(t *testing.T) {
	_, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "write"), nil,
		&grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/AdjustBalance"},
		func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil })
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unknown RPC error = %v", err)
	}
}

func TestAPITokenAuthorizationAllowsTenantBoundAdmissionPreflightForAnyScopedToken(t *testing.T) {
	called := false
	_, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "streams:read"),
		&purserpb.GetTenantAdmissionStatusRequest{TenantId: "tenant-1"},
		&grpcpkg.UnaryServerInfo{FullMethod: "/purser.BillingService/GetTenantAdmissionStatus"},
		func(context.Context, any) (any, error) { called = true; return nil, nil })
	if err != nil || !called {
		t.Fatalf("admission preflight = called %v, err %v", called, err)
	}
}

func TestAPITokenAuthorizationProtectsFullBillingStatus(t *testing.T) {
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.BillingService/GetTenantBillingStatus"}
	req := &purserpb.GetTenantBillingStatusRequest{TenantId: "tenant-1"}
	handler := func(context.Context, any) (any, error) { return nil, nil }
	if _, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "streams:read"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-billing token error = %v, want PermissionDenied", err)
	}
	if _, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "billing:read"), req, method, handler); err != nil {
		t.Fatal(err)
	}
}

func TestAPITokenAuthorizationRejectsCoarseWriteScope(t *testing.T) {
	called := false
	_, err := apiTokenAuthorizationInterceptor()(
		delegatedBillingContext("tenant-1", "write"),
		&purserpb.ChangeBillingTierRequest{TenantId: "tenant-1"},
		&grpcpkg.UnaryServerInfo{FullMethod: "/purser.PrepaidService/ChangeBillingTier"},
		func(context.Context, any) (any, error) { called = true; return nil, nil },
	)
	if status.Code(err) != codes.PermissionDenied || called {
		t.Fatalf("coarse write = called %v, err %v", called, err)
	}
}

func TestAPITokenAuthorizationRequiresInfrastructureScopeForClusterPricing(t *testing.T) {
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.ClusterPricingService/SetClusterPricing"}
	req := &purserpb.SetClusterPricingRequest{}
	handler := func(context.Context, any) (any, error) { return nil, nil }
	if _, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "billing:write"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("billing-only token error = %v, want PermissionDenied", err)
	}
	if _, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "infrastructure:write"), req, method, handler); err != nil {
		t.Fatal(err)
	}
}

func TestClusterPricingAuthorizationUsesQuartermasterOwnership(t *testing.T) {
	owner := "tenant-1"
	stub := &commercialQuartermasterStub{cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "cluster-1", OwnerTenantId: &owner}}
	server := &PurserServer{quartermasterClient: stub}
	if err := server.authorizeClusterPricingMutation(billingActorContext("jwt", owner, "owner"), "cluster-1"); err != nil {
		t.Fatalf("cluster owner denied: %v", err)
	}
	if err := server.authorizeClusterPricingMutation(billingActorContext("jwt", "tenant-2", "owner"), "cluster-1"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign owner status = %v, want PermissionDenied", status.Code(err))
	}
	if err := server.authorizeClusterPricingMutation(billingActorContext("jwt", owner, "member"), "cluster-1"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestClusterSubscriptionUsesInfrastructureScopeAndOwnerRole(t *testing.T) {
	method := &grpcpkg.UnaryServerInfo{FullMethod: "/purser.ClusterPricingService/CreateClusterSubscription"}
	req := &purserpb.CreateClusterSubscriptionRequest{TenantId: "tenant-1", ClusterId: "cluster-1"}
	handler := func(context.Context, any) (any, error) { return "called", nil }
	if _, err := apiTokenAuthorizationInterceptor()(delegatedBillingContext("tenant-1", "billing:write"), req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("billing scope status = %v, want PermissionDenied", status.Code(err))
	}
	ctx := delegatedBillingContext("tenant-1", "infrastructure:write")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	if _, err := apiTokenAuthorizationInterceptor()(ctx, req, method, handler); err != nil {
		t.Fatalf("infrastructure scope denied: %v", err)
	}
	if _, err := billingMutationAuthorizationInterceptor()(ctx, req, method, handler); err != nil {
		t.Fatalf("tenant owner denied: %v", err)
	}
	member := context.WithValue(ctx, ctxkeys.KeyRole, "member")
	if _, err := billingMutationAuthorizationInterceptor()(member, req, method, handler); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member status = %v, want PermissionDenied", status.Code(err))
	}
}
