package authz

import (
	"context"
	"testing"
)

func TestDefaultAuthorizerPlatformAdmin(t *testing.T) {
	cases := []struct {
		name string
		id   Identity
		want bool
	}{
		{"operator allowed", Identity{PlatformOperator: true}, true},
		{"owner-without-grant denied", Identity{Role: "owner", TenantID: "t1"}, false},
		{"plain user denied", Identity{Role: "member"}, false},
		{"empty identity denied", Identity{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Default.Can(context.Background(), tc.id, ActionAccessPlatformAdmin, Resource{}).Allow
			if got != tc.want {
				t.Errorf("Can(platform.admin) = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultAuthorizerAdminMistNode(t *testing.T) {
	cases := []struct {
		name  string
		id    Identity
		owner string
		want  bool
	}{
		{"node-owner owner", Identity{Role: "owner", TenantID: "acme"}, "acme", true},
		{"node-owner admin", Identity{Role: "admin", TenantID: "acme"}, "acme", true},
		{"node-owner member denied", Identity{Role: "member", TenantID: "acme"}, "acme", false},
		{"other-tenant owner denied", Identity{Role: "owner", TenantID: "evil"}, "acme", false},
		{"platform operator break-glass", Identity{PlatformOperator: true, Role: "member", TenantID: "x"}, "acme", true},
		{"operator break-glass no owner", Identity{PlatformOperator: true}, "", true},
		{"empty owner non-operator denied", Identity{Role: "owner", TenantID: "acme"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Default.Can(context.Background(), tc.id, ActionAdminMistNode, Resource{OwnerTenantID: tc.owner}).Allow
			if got != tc.want {
				t.Errorf("Can(mist.node.admin) = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultAuthorizerUnknownActionDenies(t *testing.T) {
	if Default.Can(context.Background(), Identity{PlatformOperator: true}, Action("nonsense"), Resource{}).Allow {
		t.Error("unknown action must fail closed")
	}
}

func TestDefaultAuthorizerTenantLifecycleActions(t *testing.T) {
	actions := []Action{ActionManageTenantSettings, ActionManageEdgeCluster, ActionManageBilling, ActionManageStreams, ActionManageDeveloperTokens, ActionManageWebhooks, ActionReadPrivateInfrastructure}
	cases := []struct {
		name string
		id   Identity
		want bool
	}{
		{name: "same tenant owner", id: Identity{TenantID: "tenant-a", Role: "owner"}, want: true},
		{name: "same tenant admin", id: Identity{TenantID: "tenant-a", Role: "admin"}, want: true},
		{name: "RFC roles cannot widen tenant role", id: Identity{TenantID: "tenant-a", Role: "member", Roles: []string{"admin"}}, want: false},
		{name: "same tenant member", id: Identity{TenantID: "tenant-a", Role: "member"}, want: false},
		{name: "foreign tenant owner", id: Identity{TenantID: "tenant-b", Role: "owner"}, want: false},
		{name: "platform operator", id: Identity{PlatformOperator: true}, want: true},
		{name: "empty identity", id: Identity{}, want: false},
	}
	for _, action := range actions {
		for _, tc := range cases {
			t.Run(string(action)+"/"+tc.name, func(t *testing.T) {
				got := Default.Can(context.Background(), tc.id, action, Resource{OwnerTenantID: "tenant-a"}).Allow
				if got != tc.want {
					t.Fatalf("Can(%s) = %v, want %v", action, got, tc.want)
				}
			})
		}
	}
}
