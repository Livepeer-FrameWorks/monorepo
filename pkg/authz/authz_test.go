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
