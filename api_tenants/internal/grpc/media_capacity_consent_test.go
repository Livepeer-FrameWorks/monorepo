package grpc

import (
	"context"
	"testing"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func consentActor(tenant, actor, role string) context.Context {
	return context.WithValue(tenantCtx(tenant, role), ctxkeys.KeyUserID, actor)
}

func TestCapacityConsentActorAuthorization(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111171"
	for _, test := range []struct {
		name, role, auth, permission string
		manage, allowed              bool
	}{
		{"owner write", "owner", "jwt", "", true, true},
		{"admin write", "admin", "jwt", "", true, true},
		{"member read", "member", "jwt", "", false, true},
		{"viewer read", "viewer", "jwt", "", false, true},
		{"member write", "member", "jwt", "", true, false},
		{"viewer write", "viewer", "jwt", "", true, false},
		{"service identity", "owner", "service", "placement:write", true, false},
		{"scoped write", "owner", "api_token", "placement:write", true, true},
		{"scoped read", "viewer", "api_token", "placement:read", false, true},
		{"infra is not consent", "owner", "api_token", "infrastructure:write", true, false},
		{"read is not write", "owner", "api_token", "placement:read", true, false},
		{"scope is not role", "member", "api_token", "placement:write", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(consentActor(tenant, "actor", test.role), ctxkeys.KeyAuthType, test.auth)
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{test.permission})
			_, err := consentScope(ctx, "owned", test.manage)
			if (err == nil) != test.allowed {
				t.Fatalf("authorization: %v", err)
			}
		})
	}
	server := &QuartermasterServer{}
	if _, err := server.GetClusterMediaConsent(context.Background(), nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous read: %v", err)
	}
	if _, err := server.ApplyClusterMediaConsentChange(serviceCtx(), &quartermasterpb.ApplyClusterMediaConsentRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("service write: %v", err)
	}
}

func TestCapacityConsentReviewDescribesEveryChangedPermission(t *testing.T) {
	scope := quartermasterdb.MediaConsentScope{TenantID: "11111111-1111-4111-8111-111111111171", ClusterID: "owned"}
	before := quartermasterdb.MediaConsent{ClusterRecordID: "11111111-1111-4111-8111-111111111172", AllowIngest: true, AllowServe: true, AllowExternalSource: true}
	after := before
	after.Revision, after.AllowIngest, after.AllowServe, after.AllowExternalSource = 1, false, false, false
	binding, review, err := buildConsentReview(scope, "actor", before, after)
	if err != nil || len(binding.RequiredWarnings) != 3 || len(review.GetDifferences()) != 3 || len(review.GetWarnings()) != 3 || review.GetImpact().GetComplete() {
		t.Fatalf("review: %+v, %v", review, err)
	}
	unchanged := before
	unchanged.Revision = 1
	if _, _, err := buildConsentReview(scope, "actor", before, unchanged); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("no-effect review: %v", err)
	}
	after.ClusterRecordID = "11111111-1111-4111-8111-111111111173"
	if _, _, err := buildConsentReview(scope, "actor", before, after); status.Code(err) != codes.Aborted {
		t.Fatalf("recreated scope: %v", err)
	}
}
