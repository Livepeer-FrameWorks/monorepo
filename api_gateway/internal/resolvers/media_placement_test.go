package resolvers

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementAPITestInput() model.ReviewMediaPlacementChangeInput {
	return model.ReviewMediaPlacementChangeInput{Scope: &model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindStream, StreamID: strPtr("stream")}, ExpectedRevision: "9007199254740993", ExpectedParentRevision: "7",
		Updates: []*model.MediaPlacementVerbUpdateInput{{Verb: model.MediaPlacementVerbServe, Kind: model.MediaPlacementUpdateKindSet, Rules: &model.MediaPlacementRulesInput{SchemaVersion: 1, Constraints: &model.MediaPlacementConstraintsInput{Allow: &model.MediaPlacementAllowInput{Any: []*model.MediaPlacementSelectorInput{}}}, Preferences: &model.MediaPlacementPreferencesInput{Groups: []*model.MediaPlacementGroupInput{}}}}}}
}

func placementAPITestContext() context.Context {
	return context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyUserID, "user")
}

func TestPlacementAPIInputPreservesExactIntent(t *testing.T) {
	input := placementAPITestInput()
	wire, err := placementReviewInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if wire.GetExpectedRevision() != 9007199254740993 || wire.GetExpectedParentRevision() != 7 || wire.GetScope().GetStreamId() != "stream" {
		t.Fatal("revision precision or scope was lost")
	}
	rules := wire.GetUpdates()[0].GetRules()
	if rules.GetConstraints().GetAllow() == nil || len(rules.GetConstraints().GetAllow().GetAny()) != 0 || rules.GetPreferences() == nil || len(rules.GetPreferences().GetGroups()) != 0 {
		t.Fatal("explicit deny-all became inheritance")
	}
	input.Updates[0].Rules.Constraints.Allow, input.Updates[0].Rules.Preferences = nil, nil
	wire, err = placementReviewInput(input)
	if err != nil || wire.GetUpdates()[0].GetRules().GetPreferences() != nil || wire.GetUpdates()[0].GetRules().GetConstraints().GetAllow() != nil {
		t.Fatalf("inheritance became deny-all: %v", err)
	}
	input.Updates[0].Kind, input.Updates[0].Rules = model.MediaPlacementUpdateKindClear, nil
	wire, err = placementReviewInput(input)
	if err != nil || wire.GetUpdates()[0].GetKind() != placementpb.UpdateKind_UPDATE_KIND_CLEAR || wire.GetUpdates()[0].GetRules() != nil {
		t.Fatalf("clear was not preserved: %v", err)
	}
}

func TestPlacementAPIInputRejectsInvalidShapes(t *testing.T) {
	for _, scenario := range []string{"negative", "overflow", "precision notation", "leading zero", "duplicate verb", "unknown verb", "clear rules", "nil selector", "unknown class", "schema overflow", "nonfinite distance", "tenant parent"} {
		t.Run(scenario, func(t *testing.T) {
			input := placementAPITestInput()
			switch scenario {
			case "negative":
				input.ExpectedRevision = "-1"
			case "overflow":
				input.ExpectedRevision = "9223372036854775808"
			case "precision notation":
				input.ExpectedRevision = "1e3"
			case "leading zero":
				input.ExpectedRevision = "01"
			case "duplicate verb":
				input.Updates = append(input.Updates, input.Updates[0])
			case "unknown verb":
				input.Updates[0].Verb = "STORAGE"
			case "clear rules":
				input.Updates[0].Kind = model.MediaPlacementUpdateKindClear
			case "nil selector":
				input.Updates[0].Rules.Constraints.Deny = []*model.MediaPlacementSelectorInput{nil}
			case "unknown class":
				input.Updates[0].Rules.Constraints.Deny = []*model.MediaPlacementSelectorInput{{Classes: []model.MediaPlacementClass{"UNKNOWN"}}}
			case "schema overflow":
				input.Updates[0].Rules.SchemaVersion = 1<<32 + 1
			case "nonfinite distance":
				input.Updates[0].Rules.Preferences.Groups = []*model.MediaPlacementGroupInput{{ID: "one", Match: &model.MediaPlacementSelectorInput{}, Order: model.MediaPlacementOrderDistance, Spillover: model.MediaPlacementSpilloverNever, MaxDistanceKm: math.Inf(1)}}
			case "tenant parent":
				input.Scope = &model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindTenant}
			}
			if _, err := placementReviewInput(input); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestPlacementAPIReadCompilesInheritanceWithoutClaimingActivation(t *testing.T) {
	parent := &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}}}}
	child := &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{}}
	scope := &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}
	response := &placementpb.PolicyState{Scope: scope, Own: &placementpb.PolicySet{Revision: 9007199254740993, Serve: child}, Inherited: &placementpb.PolicySet{Revision: 7, Serve: parent},
		Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}, Actions: &placementpb.Actions{CanRead: true, CanManage: false}, Features: &placementpb.Features{SchemaVersion: 1}}
	fake := &clientstest.FakeCommodore{GetMediaPlacementPolicyFn: func(ctx context.Context, request *placementpb.GetPolicyRequest) (*placementpb.PolicyState, error) {
		if ctxkeys.GetTenantID(ctx) != "tenant" || !proto.Equal(request.GetScope(), scope) {
			t.Fatal("gateway replaced authenticated tenant or requested scope")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 6*time.Second {
			t.Fatal("placement RPC was not bounded")
		}
		return response, nil
	}}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	result, err := r.DoMediaPlacementPolicy(placementAPITestContext(), *placementAPITestInput().Scope)
	out, ok := result.(*model.MediaPlacementPolicyState)
	if err != nil || !ok {
		t.Fatalf("policy read failed: %+v %v", result, err)
	}
	if out.Revision != "9007199254740993" || out.ParentRevision != "7" || out.ActiveRevision != nil || out.Rollout.Status != model.MediaPlacementRolloutStatusPending || out.Actions.CanManage {
		t.Fatal("read lost revision precision or invented activation/permission")
	}
	serve := out.Verbs[1]
	compiled, err := placement.CompilePolicySets(response.Inherited, response.Own, placement.Serve)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := placement.Digest(compiled)
	if err != nil || serve.RequestedEffective.Digest != digest || len(serve.RequestedEffective.Groups) != 0 || serve.InheritedRules.Constraints.Deny[0].Classes[0] != model.MediaPlacementClassPlatformOfficial {
		t.Fatal("hard deny inheritance or effective policy digest diverged from evaluator")
	}
	if len(out.Verbs[0].RequestedEffective.Groups) != 1 {
		t.Fatal("unconfigured verb was represented as deny-all")
	}
	response.Scope = &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "another"}
	result, err = r.DoMediaPlacementPolicy(placementAPITestContext(), *placementAPITestInput().Scope)
	if _, ok := result.(*model.MediaPlacementError); err != nil || !ok {
		t.Fatal("cross-scope downstream response escaped validation")
	}
}

func TestPlacementAPIAuthAndDemoCannotReachBackend(t *testing.T) {
	for _, scenario := range []string{"anonymous", "missing user", "member write", "token no scope", "public token no scope", "demo"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := placementAPITestContext()
			switch scenario {
			case "anonymous":
				ctx = context.Background()
			case "missing user":
				ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "")
			case "member write":
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, "member")
			case "token no scope", "public token no scope":
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
				if scenario == "public token no scope" {
					ctx = context.WithValue(ctx, ctxkeys.KeyPublicAllowlisted, true)
				}
			case "demo":
				ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
			}
			fake := &clientstest.FakeCommodore{}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			result, err := r.DoReviewMediaPlacementChange(ctx, placementAPITestInput())
			if err != nil || result == nil || fake.Calls != 0 {
				t.Fatalf("unauthorized/demo review reached backend: %v", err)
			}
		})
	}
}

func TestPlacementAPITypedFailuresDoNotLeakDiagnostics(t *testing.T) {
	for code, want := range map[codes.Code]model.MediaPlacementErrorCode{codes.InvalidArgument: model.MediaPlacementErrorCodeInvalidInput, codes.Aborted: model.MediaPlacementErrorCodeRevisionConflict, codes.AlreadyExists: model.MediaPlacementErrorCodeIdempotencyConflict, codes.FailedPrecondition: model.MediaPlacementErrorCodeStaleReview, codes.Unavailable: model.MediaPlacementErrorCodeUnavailable, codes.DeadlineExceeded: model.MediaPlacementErrorCodeUnavailable, codes.ResourceExhausted: model.MediaPlacementErrorCodeRateLimited} {
		t.Run(code.String(), func(t *testing.T) {
			result := placementFailure(status.Error(code, "private-node-and-source-secret"))
			typed, ok := result.(*model.MediaPlacementError)
			if !ok || typed.Code != want {
				t.Fatalf("wrong typed failure: %+v", result)
			}
			body, err := json.Marshal(result)
			if err != nil || strings.Contains(string(body), "private-node") {
				t.Fatal("downstream diagnostic escaped public boundary")
			}
		})
	}
}

func TestPlacementAPIApplyAndRecoverKeepExactChange(t *testing.T) {
	input := placementAPITestInput()
	var saved *placementpb.ApplyChangeRequest
	receipt := &placementpb.Change{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}, IdempotencyKey: "same-key", Revision: 9007199254740994, ParentRevision: 7, Digest: "digest", CreatedAt: timestamppb.Now(), Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}}
	fake := &clientstest.FakeCommodore{
		ApplyMediaPlacementChangeFn: func(ctx context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
			if ctxkeys.GetTenantID(ctx) != "tenant" {
				t.Fatal("apply lost tenant identity")
			}
			saved = proto.CloneOf(req)
			return nil, status.Error(codes.DeadlineExceeded, "ambiguous write")
		},
		GetMediaPlacementChangeFn: func(_ context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
			if saved == nil || req.GetIdempotencyKey() != saved.GetIdempotencyKey() || !proto.Equal(req.GetScope(), saved.GetChange().GetScope()) {
				t.Fatal("recovery changed mutation identity")
			}
			return receipt, nil
		},
	}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	apply := model.ApplyMediaPlacementChangeInput{Scope: input.Scope, ExpectedRevision: input.ExpectedRevision, ExpectedParentRevision: input.ExpectedParentRevision, Updates: input.Updates, ReviewToken: "opaque-review", IdempotencyKey: "same-key", AcknowledgedWarningIds: []string{"serve_deny_all"}}
	result, err := r.DoApplyMediaPlacementChange(placementAPITestContext(), apply)
	if failure, ok := result.(*model.MediaPlacementError); err != nil || !ok || failure.Code != model.MediaPlacementErrorCodeUnavailable || fake.Calls != 1 {
		t.Fatal("ambiguous write retried or returned success")
	}
	if saved.GetReviewToken() != apply.ReviewToken || saved.GetAcknowledgedWarningIds()[0] != "serve_deny_all" || saved.GetChange().GetExpectedRevision() != 9007199254740993 {
		t.Fatal("apply changed reviewed command")
	}
	result, err = r.DoMediaPlacementChange(placementAPITestContext(), *input.Scope, apply.IdempotencyKey)
	if change, ok := result.(*model.MediaPlacementChange); err != nil || !ok || change.Revision != "9007199254740994" || change.Rollout.Status != model.MediaPlacementRolloutStatusPending || fake.Calls != 2 {
		t.Fatal("recovery lost durable receipt or claimed activation")
	}
}

func TestPlacementAPIScopedTokenReadAndWriteRemainIndependent(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "viewer"} {
		for _, scope := range []string{"placement:read", "placement:write", "infrastructure:write"} {
			t.Run(role+"/"+scope, func(t *testing.T) {
				ctx := context.WithValue(placementAPITestContext(), ctxkeys.KeyAuthType, "api_token")
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
				ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
				r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))}
				if (r.placementAccess(ctx, false) == nil) != (scope == "placement:read") {
					t.Fatal("placement read scope or tenant membership was lost")
				}
				wantManage := scope == "placement:write" && (role == "owner" || role == "admin")
				if (r.placementAccess(ctx, true) == nil) != wantManage {
					t.Fatal("placement write scope substituted for owner permission")
				}
			})
		}
	}
}

func TestPlacementAPIResponseRejectsUnknownOrOversizedState(t *testing.T) {
	for _, scenario := range []string{"revision", "active parent", "rollout", "recipient count", "schema", "missing actions", "nil response"} {
		t.Run(scenario, func(t *testing.T) {
			response := &placementpb.PolicyState{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, Own: &placementpb.PolicySet{}, Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}, Actions: &placementpb.Actions{}, Features: &placementpb.Features{SchemaVersion: 1}}
			switch scenario {
			case "revision":
				response.Own.Revision = math.MaxUint64
			case "active parent":
				response.ActiveParentRevision = math.MaxUint64
			case "rollout":
				response.Rollout.Status = 999
			case "recipient count":
				response.Rollout.RequiredRecipients = math.MaxUint32
			case "schema":
				response.Features.SchemaVersion = 999
			case "missing actions":
				response.Actions = nil
			case "nil response":
				response = nil
			}
			if _, err := placementPolicyOutput(response); err == nil {
				t.Fatal("unsupported response state was advertised as usable")
			}
		})
	}
}

func TestPlacementAPICapacityConsentRejectsServiceAndOperatorBypass(t *testing.T) {
	for _, scenario := range []string{"service", "wallet", "platform operator", "token missing scope", "demo"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := placementAPITestContext()
			switch scenario {
			case "service", "wallet":
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, scenario)
			case "platform operator":
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, "viewer")
				ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
			case "token missing scope":
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
			case "demo":
				ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
			}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithQuartermaster(&clientstest.FakeQuartermaster{}))}
			if result, err := r.DoReviewClusterMediaConsentChange(ctx, model.ReviewMediaCapacityConsentInput{}); err != nil || result == nil {
				t.Fatal("consent access did not return a typed refusal")
			}
		})
	}
}

func TestPlacementAPILegacyPinsExposeOnlyTenantScopedPins(t *testing.T) {
	fake := &clientstest.FakeCommodore{GetStreamFn: func(ctx context.Context, id string) (*commodorepb.Stream, error) {
		if ctxkeys.GetTenantID(ctx) != "tenant" || id != "stream" {
			t.Fatal("legacy pins lost tenant scope")
		}
		return &commodorepb.Stream{StreamId: "stream", StreamKey: "secret-stream-key", PullSource: &commodorepb.PullSourceView{SourceUriRedacted: "private-source", AllowedClusterIds: []string{"owned"}}}, nil
	}}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	result, err := r.DoMediaPlacementLegacyPins(placementAPITestContext(), "stream")
	pins, ok := result.(*model.MediaPlacementLegacyPins)
	if err != nil || !ok || len(pins.ClusterIds) != 1 || pins.ClusterIds[0] != "owned" || !pins.CurrentlyEnforced {
		t.Fatal("legacy source pin was not reported")
	}
	body, err := json.Marshal(result)
	if err != nil || strings.Contains(string(body), "secret-stream-key") || strings.Contains(string(body), "private-source") {
		t.Fatal("legacy pin projection exposed source credentials")
	}
}
