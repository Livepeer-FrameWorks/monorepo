package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPlacementAPIGraphQLReadReviewApplyAndRecover(t *testing.T) {
	var appliedKey string
	receipt := func() *placementpb.Change {
		return &placementpb.Change{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, Revision: 9007199254740994, IdempotencyKey: appliedKey, Digest: "review-digest", CreatedAt: timestamppb.Now(), Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING, ExistingSessionsRetained: true}}
	}
	fake := &clientstest.FakeCommodore{
		GetMediaPlacementPolicyFn: func(ctx context.Context, req *placementpb.GetPolicyRequest) (*placementpb.PolicyState, error) {
			if ctxkeys.GetTenantID(ctx) != "tenant-realpath-1" {
				t.Fatal("GraphQL failed to carry authenticated tenant")
			}
			return &placementpb.PolicyState{Scope: req.GetScope(), Own: &placementpb.PolicySet{Revision: 9007199254740993}, Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}, Actions: &placementpb.Actions{CanRead: true, CanManage: true}, Features: &placementpb.Features{SchemaVersion: 1}}, nil
		},
		ReviewMediaPlacementChangeFn: func(_ context.Context, req *placementpb.ReviewChangeRequest) (*placementpb.Review, error) {
			if req.GetExpectedRevision() != 9007199254740993 || req.GetUpdates()[0].GetRules().GetConstraints().GetAllow() == nil {
				t.Fatal("GraphQL lost exact revision or explicit allow presence")
			}
			return &placementpb.Review{ReviewToken: "opaque-review", Digest: "review-digest", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Impact: &placementpb.Impact{ExistingSessionsRetained: true}, Warnings: []*placementpb.Warning{{Id: "serve_deny_all", Message: "No new serving destination", AcknowledgementRequired: true}}}, nil
		},
		ApplyMediaPlacementChangeFn: func(_ context.Context, req *placementpb.ApplyChangeRequest) (*placementpb.Change, error) {
			if req.GetReviewToken() != "opaque-review" || len(req.GetAcknowledgedWarningIds()) != 1 || req.GetAcknowledgedWarningIds()[0] != "serve_deny_all" {
				t.Fatal("GraphQL changed reviewed acknowledgements")
			}
			appliedKey = req.GetIdempotencyKey()
			return receipt(), nil
		},
		GetMediaPlacementChangeFn: func(_ context.Context, req *placementpb.GetChangeRequest) (*placementpb.Change, error) {
			if req.GetIdempotencyKey() != appliedKey {
				t.Fatal("GraphQL changed recovery key")
			}
			return receipt(), nil
		},
	}
	srv := newRealPathTestServer(clientstest.Clients(clientstest.WithCommodore(fake)))
	input := map[string]any{"scope": map[string]any{"kind": "TENANT"}, "expectedRevision": "9007199254740993", "expectedParentRevision": "0", "updates": []any{map[string]any{"verb": "SERVE", "kind": "SET", "rules": map[string]any{"schemaVersion": 1, "constraints": map[string]any{"deny": []any{}, "allow": map[string]any{"any": []any{}}}, "preferences": map[string]any{"groups": []any{}}}}}}
	execute := func(query string, vars map[string]any, field, kind string) map[string]any {
		t.Helper()
		resp, code := tryExecuteRealPath(srv, query, vars)
		if code != 200 || len(resp.Errors) != 0 {
			t.Fatalf("GraphQL execution failed: %d %s", code, formatGraphQLErrors(resp.Errors))
		}
		var data map[string]map[string]any
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data[field]["__typename"] != kind {
			t.Fatalf("wrong GraphQL union: %s", resp.Data)
		}
		return data[field]
	}
	read := execute(`{ mediaPlacementPolicy(scope: {kind: TENANT}) { __typename ... on MediaPlacementPolicyState { revision activeRevision verbs { verb ownRules { schemaVersion } requestedEffective { digest groups { id order spillover match { clusterIds classes } } } } } } }`, nil, "mediaPlacementPolicy", "MediaPlacementPolicyState")
	if read["revision"] != "9007199254740993" || read["activeRevision"] != nil {
		t.Fatal("GraphQL truncated revision or claimed activation")
	}
	review := execute(`query($input: ReviewMediaPlacementChangeInput!) { reviewMediaPlacementChange(input: $input) { __typename ... on MediaPlacementReview { reviewToken warnings { id acknowledgementRequired severity } impact { complete existingSessionsRetained } } } }`, map[string]any{"input": input}, "reviewMediaPlacementChange", "MediaPlacementReview")
	if review["reviewToken"] != "opaque-review" || review["impact"].(map[string]any)["complete"] != false {
		t.Fatal("GraphQL lost review token or invented complete impact")
	}
	input["reviewToken"], input["idempotencyKey"], input["acknowledgedWarningIds"] = "opaque-review", "same-key", []string{"serve_deny_all"}
	for _, operation := range []struct {
		query, field string
		vars         map[string]any
	}{
		{`mutation($input: ApplyMediaPlacementChangeInput!) { applyMediaPlacementChange(input: $input) { __typename ... on MediaPlacementChange { revision rollout { status } } } }`, "applyMediaPlacementChange", map[string]any{"input": input}},
		{`{ mediaPlacementChange(scope: {kind: TENANT}, idempotencyKey: "same-key") { __typename ... on MediaPlacementChange { revision rollout { status } } } }`, "mediaPlacementChange", nil},
	} {
		change := execute(operation.query, operation.vars, operation.field, "MediaPlacementChange")
		if change["revision"] != "9007199254740994" || change["rollout"].(map[string]any)["status"] != "PENDING" {
			t.Fatal("GraphQL changed receipt revision or rollout")
		}
	}
	if fake.Calls != 4 {
		t.Fatalf("unexpected backend calls: %d", fake.Calls)
	}
}

func TestPlacementAPIGraphQLConflictIsTyped(t *testing.T) {
	fake := &clientstest.FakeCommodore{ReviewMediaPlacementChangeFn: func(context.Context, *placementpb.ReviewChangeRequest) (*placementpb.Review, error) {
		return nil, status.Error(codes.Aborted, "private backend diagnostic")
	}}
	srv := newRealPathTestServer(clientstest.Clients(clientstest.WithCommodore(fake)))
	response, code := tryExecuteRealPath(srv, `{ reviewMediaPlacementChange(input: {scope: {kind: TENANT}, expectedRevision: "0", expectedParentRevision: "0", updates: [{verb: INGEST, kind: CLEAR}]}) { __typename ... on MediaPlacementError { code fields { path } } } }`, nil)
	if code != 200 || len(response.Errors) != 0 {
		t.Fatalf("conflict escaped typed union: %s", formatGraphQLErrors(response.Errors))
	}
	var data struct {
		ReviewMediaPlacementChange struct {
			Typename string `json:"__typename"`
			Code     string
			Fields   []any
		}
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.ReviewMediaPlacementChange.Typename != "MediaPlacementError" || data.ReviewMediaPlacementChange.Code != "REVISION_CONFLICT" || data.ReviewMediaPlacementChange.Fields == nil {
		t.Fatalf("wrong conflict representation: %s", response.Data)
	}
}

func TestPlacementAPIIngestGraphFieldPreservesProtocol(t *testing.T) {
	for _, protocol := range []model.MediaIngestProtocol{model.MediaIngestProtocolWhip, model.MediaIngestProtocolRtmp, model.MediaIngestProtocolSrt} {
		t.Run(string(protocol), func(t *testing.T) {
			fake := &clientstest.FakeCommodore{ResolveIngestEndpointFn: func(_ context.Context, _ string, _ string, got sharedpb.IngestProtocol) (*sharedpb.IngestEndpointResponse, error) {
				if got.String() != "INGEST_PROTOCOL_"+string(protocol) {
					t.Fatalf("GraphQL dropped protocol: %v", got)
				}
				return &sharedpb.IngestEndpointResponse{}, nil
			}}
			r := &queryResolver{&Resolver{Resolver: &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}}}
			if _, err := r.ResolveIngestEndpoint(t.Context(), "stream-key", &protocol); err != nil || fake.Calls != 1 {
				t.Fatalf("protocol forwarding failed: %v", err)
			}
		})
	}
}
