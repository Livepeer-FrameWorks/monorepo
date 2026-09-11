package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type placementConsentClient struct {
	quartermaster.Interface
	t     *testing.T
	calls int
}

func (f *placementConsentClient) check(ctx context.Context) {
	f.t.Helper()
	f.calls++
	if ctxkeys.GetTenantID(ctx) != "tenant-realpath-1" || ctxkeys.GetUserID(ctx) != "user-realpath-1" {
		f.t.Fatal("capacity owner RPC lost tenant actor")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 6*time.Second {
		f.t.Fatal("capacity owner RPC is unbounded")
	}
}

func (f *placementConsentClient) GetClusterMediaConsent(ctx context.Context, req *quartermasterpb.GetClusterMediaConsentRequest) (*quartermasterpb.ClusterMediaConsentState, error) {
	f.check(ctx)
	if req.GetClusterId() != "owned" {
		return nil, status.Error(codes.NotFound, "private ownership diagnostic")
	}
	return &quartermasterpb.ClusterMediaConsentState{ClusterId: "owned", Consent: &placementpb.CapacityConsent{Revision: 9007199254740993, AllowIngest: false, AllowServe: true, AllowExternalSource: false}, CanManage: true, Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}}, nil
}

func (f *placementConsentClient) ReviewClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.ReviewClusterMediaConsentRequest) (*placementpb.Review, error) {
	f.check(ctx)
	if req.GetClusterId() != "owned" || req.GetExpectedRevision() != 9007199254740993 || req.GetAllowIngest() || !req.GetAllowServe() || req.GetAllowExternalSource() {
		f.t.Fatal("consent review changed scope, exact revision or explicit false flags")
	}
	return &placementpb.Review{ReviewToken: "owner-review", Digest: "owner-digest", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Impact: &placementpb.Impact{Complete: false, ExistingSessionsRetained: true}, Warnings: []*placementpb.Warning{{Id: "external-source", Message: "External source permission changes", AcknowledgementRequired: true}}}, nil
}

func (f *placementConsentClient) ApplyClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.ApplyClusterMediaConsentRequest) (*quartermasterpb.ClusterMediaConsentChange, error) {
	f.check(ctx)
	if req.GetChange().GetClusterId() != "owned" || req.GetChange().GetExpectedRevision() != 9007199254740993 || req.GetChange().GetAllowIngest() || !req.GetChange().GetAllowServe() || req.GetChange().GetAllowExternalSource() || req.GetReviewToken() != "owner-review" || req.GetIdempotencyKey() != "same-owner-key" || len(req.GetAcknowledgedWarningIds()) != 1 || req.GetAcknowledgedWarningIds()[0] != "external-source" {
		f.t.Fatal("consent apply changed reviewed command")
	}
	return consentGraphReceipt(), nil
}

func (f *placementConsentClient) GetClusterMediaConsentChange(ctx context.Context, req *quartermasterpb.GetClusterMediaConsentChangeRequest) (*quartermasterpb.ClusterMediaConsentChange, error) {
	f.check(ctx)
	if req.GetClusterId() != "owned" || req.GetIdempotencyKey() != "same-owner-key" {
		f.t.Fatal("consent recovery changed scope or key")
	}
	return consentGraphReceipt(), nil
}

func consentGraphReceipt() *quartermasterpb.ClusterMediaConsentChange {
	return &quartermasterpb.ClusterMediaConsentChange{ClusterId: "owned", IdempotencyKey: "same-owner-key", Revision: 9007199254740994, Digest: "owner-digest", CreatedAt: timestamppb.Now(), Rollout: &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING}}
}

func TestPlacementAPIConsentGraphQLWorkflow(t *testing.T) {
	fake := &placementConsentClient{t: t}
	srv := newRealPathTestServer(&clients.ServiceClients{Quartermaster: fake})
	for _, operation := range []struct{ query, field, kind string }{
		{`{ clusterMediaConsent(clusterId: "owned") { __typename ... on MediaCapacityConsent { revision allowIngest allowServe allowExternalSource canManage rollout { status } } } }`, "clusterMediaConsent", "MediaCapacityConsent"},
		{`{ reviewClusterMediaConsentChange(input: {clusterId: "owned", expectedRevision: "9007199254740993", allowIngest: false, allowServe: true, allowExternalSource: false}) { __typename ... on MediaPlacementReview { reviewToken impact { complete } warnings { acknowledgementRequired } } } }`, "reviewClusterMediaConsentChange", "MediaPlacementReview"},
		{`mutation { applyClusterMediaConsentChange(input: {clusterId: "owned", expectedRevision: "9007199254740993", allowIngest: false, allowServe: true, allowExternalSource: false, reviewToken: "owner-review", idempotencyKey: "same-owner-key", acknowledgedWarningIds: ["external-source"]}) { __typename ... on MediaCapacityConsentChange { revision rollout { status } } } }`, "applyClusterMediaConsentChange", "MediaCapacityConsentChange"},
		{`{ clusterMediaConsentChange(clusterId: "owned", idempotencyKey: "same-owner-key") { __typename ... on MediaCapacityConsentChange { revision rollout { status } } } }`, "clusterMediaConsentChange", "MediaCapacityConsentChange"},
		{`{ clusterMediaConsent(clusterId: "another") { __typename ... on NotFoundError { message } } }`, "clusterMediaConsent", "NotFoundError"},
	} {
		response, code := tryExecuteRealPath(srv, operation.query, nil)
		if code != 200 || len(response.Errors) != 0 {
			t.Fatalf("consent GraphQL failed: %d %s", code, formatGraphQLErrors(response.Errors))
		}
		var data map[string]map[string]any
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		out := data[operation.field]
		if out["__typename"] != operation.kind {
			t.Fatalf("wrong consent result: %s", response.Data)
		}
		if operation.kind == "MediaCapacityConsentChange" && (out["revision"] != "9007199254740994" || out["rollout"].(map[string]any)["status"] != "PENDING") {
			t.Fatal("consent receipt changed revision or claimed enforcement")
		}
		if operation.kind == "MediaCapacityConsent" && (out["revision"] != "9007199254740993" || out["allowIngest"] != false || out["allowServe"] != true || out["allowExternalSource"] != false) {
			t.Fatal("consent read changed flags or precision")
		}
	}
	if fake.calls != 5 {
		t.Fatalf("unexpected capacity owner calls: %d", fake.calls)
	}
}
