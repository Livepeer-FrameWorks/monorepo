//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type placementManagementOwnerFixture struct{ version atomic.Int32 }

func (fixture *placementManagementOwnerFixture) GetTenant(_ context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: tenantID, IsActive: true}}, nil
}
func (*placementManagementOwnerFixture) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return &quartermasterpb.GetTenantEntitlementResponse{}, nil
}
func (*placementManagementOwnerFixture) ListActiveTenants(context.Context) ([]string, error) {
	return nil, nil
}
func (*placementManagementOwnerFixture) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}, nil
}
func (fixture *placementManagementOwnerFixture) GetTenantAdmissionStatus(context.Context, string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	return &purserpb.GetTenantAdmissionStatusResponse{TierLevel: fixture.version.Load()}, nil
}

func TestMediaPlacementManagement_RealPG(t *testing.T) {
	testMediaPlacementManagement(t, startCommodoreRealPG(t))
}

func TestMediaPlacementManagement_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "commodore_placement_management")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	baseline, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, schemaErr := db.Exec(string(baseline)); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	testMediaPlacementManagement(t, db)
}

func testMediaPlacementManagement(t *testing.T, db *sql.DB) {
	t.Helper()
	const tenantID = "10000000-0000-4000-8000-000000000081"
	const actorID = "20000000-0000-4000-8000-000000000081"
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner := &placementManagementOwnerFixture{}
	server := &CommodoreServer{db: db, mediaAuthorityKeyID: "placement-key", mediaAuthorityPrivateKey: private, authorityTenantSource: owner, authorityBillingSource: owner}
	ctx := ctxAs(actorID, tenantID, "owner")
	scope := &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}
	state, err := server.GetMediaPlacementPolicy(ctx, &placementpb.GetPolicyRequest{Scope: scope})
	if err != nil || state.GetOwn().GetRevision() != 0 || state.GetRollout().GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED || !state.GetActions().GetCanManage() {
		t.Fatalf("initial policy: %+v %v", state, err)
	}
	change := &placementpb.ReviewChangeRequest{Scope: scope, Updates: []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}}}}}}}
	review, err := server.ReviewMediaPlacementChange(ctx, change)
	if err != nil || len(review.GetDifferences()) == 0 || review.GetReviewToken() == "" || review.GetImpact().GetComplete() {
		t.Fatalf("review: %+v %v", review, err)
	}
	var count int
	if queryErr := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_policies WHERE tenant_id=$1`, tenantID).Scan(&count); queryErr != nil || count != 0 {
		t.Fatalf("review mutated policy: %d %v", count, queryErr)
	}
	apply := &placementpb.ApplyChangeRequest{Change: change, ReviewToken: review.GetReviewToken(), IdempotencyKey: "first"}
	first, err := server.ApplyMediaPlacementChange(ctx, apply)
	if err != nil || first.GetRevision() != 1 || first.GetRollout().GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING {
		t.Fatalf("apply: %+v %v", first, err)
	}
	state, err = server.GetMediaPlacementPolicy(ctx, &placementpb.GetPolicyRequest{Scope: scope})
	if err != nil || state.GetOwn().GetRevision() != 1 || state.GetActive() != nil {
		t.Fatalf("intent incorrectly advertised active: %+v %v", state, err)
	}

	// Rotation and unavailable owner RPCs cannot invalidate an immutable receipt.
	server.mediaAuthorityPrivateKey, server.authorityTenantSource, server.authorityBillingSource = nil, nil, nil
	withoutToken := proto.CloneOf(apply)
	withoutToken.ReviewToken = ""
	repeated, err := server.ApplyMediaPlacementChange(ctx, withoutToken)
	if err != nil || !proto.Equal(first, repeated) {
		t.Fatalf("committed recovery depended on old signing key: %+v %v", repeated, err)
	}
	altered := proto.CloneOf(withoutToken)
	altered.AcknowledgedWarningIds = []string{"invented"}
	if _, changedErr := server.ApplyMediaPlacementChange(ctx, altered); status.Code(changedErr) != codes.AlreadyExists {
		t.Fatalf("changed acknowledgements reused key: %v", changedErr)
	}
	altered = proto.CloneOf(withoutToken)
	altered.Change.Updates[0].Rules = &placementpb.Rules{SchemaVersion: 1}
	if _, changedErr := server.ApplyMediaPlacementChange(ctx, altered); status.Code(changedErr) != codes.AlreadyExists {
		t.Fatalf("changed rules reused key: %v", changedErr)
	}
	if _, actorErr := server.ApplyMediaPlacementChange(ctxAs("20000000-0000-4000-8000-000000000082", tenantID, "owner"), withoutToken); status.Code(actorErr) != codes.AlreadyExists {
		t.Fatalf("another actor reused key: %v", actorErr)
	}
	server.mediaAuthorityPrivateKey, server.authorityTenantSource, server.authorityBillingSource = private, owner, owner

	denyAll := &placementpb.ReviewChangeRequest{Scope: scope, ExpectedRevision: 1, Updates: []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{}}}}}
	denyReview, err := server.ReviewMediaPlacementChange(ctx, denyAll)
	if err != nil {
		t.Fatal(err)
	}
	denyApply := &placementpb.ApplyChangeRequest{Change: denyAll, ReviewToken: denyReview.GetReviewToken(), IdempotencyKey: "deny-all"}
	if _, ackErr := server.ApplyMediaPlacementChange(ctx, denyApply); status.Code(ackErr) != codes.InvalidArgument {
		t.Fatalf("deny-all accepted without acknowledgements: %v", ackErr)
	}
	denyApply.AcknowledgedWarningIds = requiredPlacementWarnings(denyReview)
	if len(denyApply.AcknowledgedWarningIds) == 0 {
		t.Fatal("deny-all review lacked a required warning")
	}
	owner.version.Add(1)
	if _, staleErr := server.ApplyMediaPlacementChange(ctx, denyApply); status.Code(staleErr) != codes.FailedPrecondition {
		t.Fatalf("changed owner facts accepted with old review: %v", staleErr)
	}
	denyReview, err = server.ReviewMediaPlacementChange(ctx, denyAll)
	if err != nil {
		t.Fatal(err)
	}
	denyApply.ReviewToken, denyApply.AcknowledgedWarningIds = denyReview.GetReviewToken(), requiredPlacementWarnings(denyReview)
	second, err := server.ApplyMediaPlacementChange(ctx, denyApply)
	if err != nil || second.GetRevision() != 2 {
		t.Fatalf("reviewed deny-all: %+v %v", second, err)
	}

	clearChange := &placementpb.ReviewChangeRequest{Scope: scope, ExpectedRevision: 2, Updates: []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}}}
	clearReview, err := server.ReviewMediaPlacementChange(ctx, clearChange)
	if err != nil {
		t.Fatal(err)
	}
	clearApply := &placementpb.ApplyChangeRequest{Change: clearChange, ReviewToken: clearReview.GetReviewToken(), IdempotencyKey: "clear", AcknowledgedWarningIds: requiredPlacementWarnings(clearReview)}
	var group sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		group.Go(func() {
			applied, applyErr := server.ApplyMediaPlacementChange(ctx, clearApply)
			if applyErr == nil && applied.GetRevision() != 3 {
				applyErr = status.Error(codes.Internal, "duplicate request created another revision")
			}
			results <- applyErr
		})
	}
	group.Wait()
	close(results)
	for applyErr := range results {
		if applyErr != nil {
			t.Fatalf("concurrent identical apply was not recovered: %v", applyErr)
		}
	}
	old, err := server.ApplyMediaPlacementChange(ctx, withoutToken)
	if err != nil || old.GetRevision() != 1 || old.GetRollout().GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_SUPERSEDED {
		t.Fatalf("historical recovery: %+v %v", old, err)
	}
	if queryErr := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_changes WHERE tenant_id=$1`, tenantID).Scan(&count); queryErr != nil || count != 3 {
		t.Fatalf("duplicate commands persisted: %d %v", count, queryErr)
	}

	member := ctxAs(actorID, tenantID, "member")
	if _, readErr := server.GetMediaPlacementPolicy(member, &placementpb.GetPolicyRequest{Scope: scope}); readErr != nil {
		t.Fatalf("member read denied: %v", readErr)
	}
	if _, deniedErr := server.ApplyMediaPlacementChange(member, clearApply); status.Code(deniedErr) != codes.PermissionDenied {
		t.Fatalf("member write accepted: %v", deniedErr)
	}
	tokenContext := context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	tokenContext = context.WithValue(tokenContext, ctxkeys.KeyPermissions, []string{"streams:write"})
	if _, deniedErr := server.ApplyMediaPlacementChange(tokenContext, clearApply); status.Code(deniedErr) != codes.PermissionDenied {
		t.Fatalf("stream-write token edited placement: %v", deniedErr)
	}
}

func requiredPlacementWarnings(review *placementpb.Review) []string {
	var ids []string
	for _, warning := range review.GetWarnings() {
		if warning.GetAcknowledgementRequired() {
			ids = append(ids, warning.GetId())
		}
	}
	return ids
}
