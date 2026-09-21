package grpcserver

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func errOf(_ any, err error) error { return err }

func TestWebhookWriteAuthorization(t *testing.T) {
	const tenant = "9dd90a31-eef1-4a0c-919c-606142491c92"
	for _, tc := range []struct {
		name, authType, role string
		permissions          []string
		operator, allow      bool
	}{
		{name: "owner", authType: "jwt", role: "owner", allow: true},
		{name: "admin", authType: "jwt", role: "admin", allow: true},
		{name: "wallet owner", authType: "wallet", role: "owner", allow: true},
		{name: "viewer", authType: "jwt", role: "viewer"},
		{name: "member", authType: "jwt", role: "member"},
		{name: "missing authentication", role: "owner"},
		{name: "scoped admin", authType: "api_token", role: "admin", permissions: []string{"developer:write"}, allow: true},
		{name: "read only admin", authType: "api_token", role: "admin", permissions: []string{"developer:read"}},
		{name: "scoped viewer", authType: "api_token", role: "viewer", permissions: []string{"developer:write"}},
		{name: "operator", authType: "jwt", operator: true, allow: true},
		{name: "scoped operator", authType: "api_token", operator: true},
		{name: "internal service", authType: "service", allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenant)
			ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, tc.authType)
			ctx = context.WithValue(ctx, ctxkeys.KeyRole, tc.role)
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, tc.permissions)
			ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, tc.operator)
			got, err := tenantForWrite(ctx)
			if tc.allow {
				if err != nil || got != tenant {
					t.Fatalf("got %q, %v", got, err)
				}
			} else if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("got %v, want PermissionDenied", err)
			}
		})
	}
}

func TestViewerJWTRefusesEveryWebhookWriteBeforeStoreAccess(t *testing.T) {
	secret := []byte("bosun-test-secret")
	token, err := auth.GenerateJWT("c60df7d5-1ab0-4a4a-b006-f064a5230554", "9dd90a31-eef1-4a0c-919c-606142491c92", "viewer@example.test", "viewer", secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token, "x-role", "owner", "x-auth-type", "service"))
	s := &Server{}
	calls := map[string]func(context.Context) error{
		"CreateWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.CreateWebhookEndpoint(ctx, &bosunpb.CreateWebhookEndpointRequest{}))
		},
		"UpdateWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.UpdateWebhookEndpoint(ctx, &bosunpb.UpdateWebhookEndpointRequest{}))
		},
		"DeleteWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.DeleteWebhookEndpoint(ctx, &bosunpb.DeleteWebhookEndpointRequest{}))
		},
		"EnableWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.EnableWebhookEndpoint(ctx, &bosunpb.EnableWebhookEndpointRequest{}))
		},
		"DisableWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.DisableWebhookEndpoint(ctx, &bosunpb.DisableWebhookEndpointRequest{}))
		},
		"RotateWebhookEndpointSecret": func(ctx context.Context) error {
			return errOf(s.RotateWebhookEndpointSecret(ctx, &bosunpb.RotateWebhookEndpointSecretRequest{}))
		},
		"TestWebhookEndpoint": func(ctx context.Context) error {
			return errOf(s.TestWebhookEndpoint(ctx, &bosunpb.TestWebhookEndpointRequest{}))
		},
		"ReplayWebhookDelivery": func(ctx context.Context) error {
			return errOf(s.ReplayWebhookDelivery(ctx, &bosunpb.ReplayWebhookDeliveryRequest{}))
		},
		"ReplayWebhookDeliveries": func(ctx context.Context) error {
			return errOf(s.ReplayWebhookDeliveries(ctx, &bosunpb.ReplayWebhookDeliveriesRequest{}))
		},
	}
	interceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{JWTSecret: secret})
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/bosun.BosunService/" + name}, func(ctx context.Context, _ any) (any, error) { return nil, call(ctx) })
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("got %v, want PermissionDenied", err)
			}
		})
	}
}

// Every RPC refuses a call without a tenant context before touching the
// store, which is nil here.
func TestEveryCallRequiresATenant(t *testing.T) {
	s := &Server{}
	for _, ctx := range []context.Context{
		context.Background(),
		context.WithValue(context.Background(), ctxkeys.KeyTenantID, "not-a-uuid"),
	} {
		calls := map[string]error{
			"List":        errOf(s.ListWebhookEndpoints(ctx, &bosunpb.ListWebhookEndpointsRequest{})),
			"Get":         errOf(s.GetWebhookEndpoint(ctx, &bosunpb.GetWebhookEndpointRequest{})),
			"Create":      errOf(s.CreateWebhookEndpoint(ctx, &bosunpb.CreateWebhookEndpointRequest{})),
			"Update":      errOf(s.UpdateWebhookEndpoint(ctx, &bosunpb.UpdateWebhookEndpointRequest{})),
			"Delete":      errOf(s.DeleteWebhookEndpoint(ctx, &bosunpb.DeleteWebhookEndpointRequest{})),
			"Enable":      errOf(s.EnableWebhookEndpoint(ctx, &bosunpb.EnableWebhookEndpointRequest{})),
			"Disable":     errOf(s.DisableWebhookEndpoint(ctx, &bosunpb.DisableWebhookEndpointRequest{})),
			"Rotate":      errOf(s.RotateWebhookEndpointSecret(ctx, &bosunpb.RotateWebhookEndpointSecretRequest{})),
			"Test":        errOf(s.TestWebhookEndpoint(ctx, &bosunpb.TestWebhookEndpointRequest{})),
			"Deliveries":  errOf(s.ListWebhookDeliveries(ctx, &bosunpb.ListWebhookDeliveriesRequest{})),
			"Delivery":    errOf(s.GetWebhookDelivery(ctx, &bosunpb.GetWebhookDeliveryRequest{})),
			"Replay":      errOf(s.ReplayWebhookDelivery(ctx, &bosunpb.ReplayWebhookDeliveryRequest{})),
			"ReplayRange": errOf(s.ReplayWebhookDeliveries(ctx, &bosunpb.ReplayWebhookDeliveriesRequest{})),
		}
		for name, err := range calls {
			if status.Code(err) != codes.PermissionDenied {
				t.Errorf("%s without a tenant = %v, want PermissionDenied", name, err)
			}
		}
	}
}
