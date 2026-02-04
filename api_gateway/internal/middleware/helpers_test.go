package middleware

import (
	"context"
	"testing"

	"frameworks/pkg/ctxkeys"
)

func TestGetUserFromContext(t *testing.T) {
	user := &UserContext{UserID: "user-1", TenantID: "tenant-1"}
	ctxWithUser := context.WithValue(context.Background(), ctxkeys.KeyUser, user)
	ctxWithWrongType := context.WithValue(context.Background(), ctxkeys.KeyUser, "not-a-user")

	cases := []struct {
		name     string
		ctx      context.Context
		expected *UserContext
	}{
		{name: "user present", ctx: ctxWithUser, expected: user},
		{name: "wrong type", ctx: ctxWithWrongType, expected: nil},
		{name: "missing", ctx: context.Background(), expected: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetUserFromContext(tc.ctx); got != tc.expected {
				t.Fatalf("GetUserFromContext() = %#v, want %#v", got, tc.expected)
			}
		})
	}
}

func TestHasServiceToken(t *testing.T) {
	cases := []struct {
		name     string
		ctx      context.Context
		expected bool
	}{
		{name: "service token", ctx: context.WithValue(context.Background(), ctxkeys.KeyServiceToken, "svc"), expected: true},
		{name: "empty service token", ctx: context.WithValue(context.Background(), ctxkeys.KeyServiceToken, ""), expected: false},
		{name: "missing", ctx: context.Background(), expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasServiceToken(tc.ctx); got != tc.expected {
				t.Fatalf("HasServiceToken() = %t, want %t", got, tc.expected)
			}
		})
	}
}

func TestHasPermission(t *testing.T) {
	cases := []struct {
		name       string
		ctx        context.Context
		permission string
		expected   bool
	}{
		{name: "empty permission", ctx: context.Background(), permission: "", expected: true},
		{name: "service token", ctx: context.WithValue(context.Background(), ctxkeys.KeyServiceToken, "svc"), permission: "read", expected: true},
		{name: "public allowlisted", ctx: context.WithValue(context.Background(), ctxkeys.KeyPublicAllowlisted, true), permission: "read", expected: true},
		{name: "jwt auth", ctx: context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), permission: "read", expected: true},
		{name: "wallet auth", ctx: context.WithValue(context.Background(), ctxkeys.KeyAuthType, "wallet"), permission: "read", expected: true},
		{name: "x402 auth", ctx: context.WithValue(context.Background(), ctxkeys.KeyAuthType, "x402"), permission: "read", expected: true},
		{
			name:       "api token with permission",
			ctx:        context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token"), ctxkeys.KeyPermissions, []string{"read"}),
			permission: "read",
			expected:   true,
		},
		{
			name:       "api token missing permission",
			ctx:        context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token"), ctxkeys.KeyPermissions, []string{"read"}),
			permission: "write",
			expected:   false,
		},
		{name: "unauthenticated", ctx: context.Background(), permission: "read", expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasPermission(tc.ctx, tc.permission); got != tc.expected {
				t.Fatalf("HasPermission(%q) = %t, want %t", tc.permission, got, tc.expected)
			}
		})
	}
}

func TestIsDemoMode(t *testing.T) {
	cases := []struct {
		name     string
		ctx      context.Context
		expected bool
	}{
		{name: "demo mode", ctx: context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true), expected: true},
		{name: "not demo mode", ctx: context.WithValue(context.Background(), ctxkeys.KeyDemoMode, false), expected: false},
		{name: "missing", ctx: context.Background(), expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDemoMode(tc.ctx); got != tc.expected {
				t.Fatalf("IsDemoMode() = %t, want %t", got, tc.expected)
			}
		})
	}
}

func TestIsAllowlistedQuery(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		expected bool
	}{
		{name: "mutation blocked", query: "mutation { serviceInstancesHealth }", expected: false},
		{name: "serviceInstancesHealth allowed", query: "query { serviceInstancesHealth }", expected: true},
		{name: "resolveViewerEndpoint allowed", query: "query { ResolveViewerEndpoint }", expected: true},
		{name: "resolveIngestEndpoint allowed", query: "query { resolveIngestEndpoint }", expected: true},
		{name: "not allowlisted", query: "query { other }", expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowlistedQuery([]byte(tc.query)); got != tc.expected {
				t.Fatalf("isAllowlistedQuery(%q) = %t, want %t", tc.query, got, tc.expected)
			}
		})
	}
}
