package ctxkeys

import (
	"context"
	"testing"
	"time"
)

func TestStringGetters(t *testing.T) {
	tests := []struct {
		name     string
		key      Key
		getter   func(context.Context) string
		value    string
		wantSet  string
		wantNone string
	}{
		{"GetTenantID", KeyTenantID, GetTenantID, "tenant-123", "tenant-123", ""},
		{"GetUserID", KeyUserID, GetUserID, "user-456", "user-456", ""},
		{"GetEmail", KeyEmail, GetEmail, "a@b.com", "a@b.com", ""},
		{"GetRole", KeyRole, GetRole, "admin", "admin", ""},
		{"GetJWTToken", KeyJWTToken, GetJWTToken, "jwt.tok.en", "jwt.tok.en", ""},
		{"GetAPIToken", KeyAPIToken, GetAPIToken, "tok_abc", "tok_abc", ""},
		{"GetAuthType", KeyAuthType, GetAuthType, "bearer", "bearer", ""},
		{"GetServiceToken", KeyServiceToken, GetServiceToken, "svc-tok", "svc-tok", ""},
		{"GetClientIP", KeyClientIP, GetClientIP, "192.168.1.1", "192.168.1.1", ""},
		{"GetWalletAddress", KeyWalletAddr, GetWalletAddress, "0xABC", "0xABC", ""},
		{"GetXPayment", KeyXPayment, GetXPayment, "x402-proof", "x402-proof", ""},
		{"GetCapability", KeyCapability, GetCapability, "stream:read", "stream:read", ""},
		{"GetClusterScope", KeyClusterScope, GetClusterScope, "cluster-1", "cluster-1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name+"_Present", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), tc.key, tc.value)
			if got := tc.getter(ctx); got != tc.wantSet {
				t.Fatalf("got %q, want %q", got, tc.wantSet)
			}
		})
		t.Run(tc.name+"_Missing", func(t *testing.T) {
			if got := tc.getter(context.Background()); got != tc.wantNone {
				t.Fatalf("got %q, want %q", got, tc.wantNone)
			}
		})
		t.Run(tc.name+"_WrongType", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), tc.key, 42)
			if got := tc.getter(ctx); got != tc.wantNone {
				t.Fatalf("got %q, want %q on wrong type", got, tc.wantNone)
			}
		})
	}
}

func TestBoolGetters(t *testing.T) {
	tests := []struct {
		name   string
		key    Key
		getter func(context.Context) bool
	}{
		{"IsDemoMode", KeyDemoMode, IsDemoMode},
		{"IsX402Processed", KeyX402Processed, IsX402Processed},
		{"IsX402AuthOnly", KeyX402AuthOnly, IsX402AuthOnly},
		{"IsPublicAllowlisted", KeyPublicAllowlisted, IsPublicAllowlisted},
		{"IsReadOnly", KeyReadOnly, IsReadOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name+"_True", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), tc.key, true)
			if !tc.getter(ctx) {
				t.Fatal("expected true")
			}
		})
		t.Run(tc.name+"_False", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), tc.key, false)
			if tc.getter(ctx) {
				t.Fatal("expected false")
			}
		})
		t.Run(tc.name+"_Missing", func(t *testing.T) {
			if tc.getter(context.Background()) {
				t.Fatal("expected false when missing")
			}
		})
		t.Run(tc.name+"_WrongType", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), tc.key, "true")
			if tc.getter(ctx) {
				t.Fatal("expected false on wrong type")
			}
		})
	}
}

func TestGetJWTExpiresAt(t *testing.T) {
	t.Run("Present", func(t *testing.T) {
		exp := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
		ctx := context.WithValue(context.Background(), KeyJWTExpiresAt, exp)
		got, ok := GetJWTExpiresAt(ctx)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !got.Equal(exp) {
			t.Fatalf("got %v, want %v", got, exp)
		}
	})
	t.Run("Missing", func(t *testing.T) {
		got, ok := GetJWTExpiresAt(context.Background())
		if ok {
			t.Fatal("expected ok=false")
		}
		if !got.IsZero() {
			t.Fatalf("expected zero time, got %v", got)
		}
	})
	t.Run("WrongType", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), KeyJWTExpiresAt, "not-a-time")
		_, ok := GetJWTExpiresAt(ctx)
		if ok {
			t.Fatal("expected ok=false on wrong type")
		}
	})
}

func TestGetPermissions(t *testing.T) {
	t.Run("Present", func(t *testing.T) {
		perms := []string{"read", "write", "admin"}
		ctx := context.WithValue(context.Background(), KeyPermissions, perms)
		got := GetPermissions(ctx)
		if len(got) != 3 || got[0] != "read" || got[1] != "write" || got[2] != "admin" {
			t.Fatalf("got %v, want %v", got, perms)
		}
	})
	t.Run("Empty", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), KeyPermissions, []string{})
		got := GetPermissions(ctx)
		if got == nil || len(got) != 0 {
			t.Fatalf("expected empty slice, got %v", got)
		}
	})
	t.Run("Missing", func(t *testing.T) {
		got := GetPermissions(context.Background())
		if got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})
	t.Run("WrongType", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), KeyPermissions, "not-a-slice")
		got := GetPermissions(ctx)
		if got != nil {
			t.Fatalf("expected nil on wrong type, got %v", got)
		}
	})
}
