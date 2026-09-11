package graph

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPlacementAPIPreviewGraphQLSerializedRead(t *testing.T) {
	for _, sourceEvaluated := range []bool{false, true} {
		fake := &clientstest.FakeCommodore{PreviewMediaPlacementFn: func(ctx context.Context, req *placementpb.PreviewRequest) (*placementpb.Preview, error) {
			if ctxkeys.GetTenantID(ctx) != "tenant-realpath-1" || req.GetScope().GetStreamId() != "stream" || req.GetVerb() != placementpb.Verb_VERB_SERVE || req.GetExpectedRevision() != 9007199254740993 || req.GetExpectedParentRevision() != 7 || req.GetDraftUpdate().GetKind() != placementpb.UpdateKind_UPDATE_KIND_CLEAR || req.GetCoordinates().GetLongitude() != -77 {
				t.Fatal("GraphQL changed draft preview identity or precision")
			}
			now := time.Now().UTC()
			return &placementpb.Preview{Scope: req.Scope, Verb: req.Verb, Revision: req.GetExpectedRevision(), ParentRevision: req.GetExpectedParentRevision(), Digest: strings.Repeat("a", 64), Reason: "selected", Complete: true, SourceEvaluated: sourceEvaluated, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(20 * time.Second)), Selected: &placementpb.PreviewCandidate{ClusterId: "us", ClusterName: "US", Reason: "selected", RequiresSourcePull: sourceEvaluated}}, nil
		}}
		srv := newRealPathTestServer(clientstest.Clients(clientstest.WithCommodore(fake)))
		response, code := tryExecuteRealPath(srv, `{ previewMediaPlacement(input: {scope: {kind: STREAM, streamId: "stream"}, verb: SERVE, expectedRevision: "9007199254740993", expectedParentRevision: "7", coordinates: {latitude: 39, longitude: -77}, draftUpdate: {verb: SERVE, kind: CLEAR}}) { __typename ... on MediaPlacementPreview { revision parentRevision complete sourceEvaluated candidates { clusterId } transitions { reason } selected { clusterId nodeId distanceKm requiresSourcePull } } ... on MediaPlacementError { code message } } }`, nil)
		if code != 200 || len(response.Errors) != 0 {
			t.Fatalf("GraphQL preview failed: %d %s", code, formatGraphQLErrors(response.Errors))
		}
		var data struct {
			PreviewMediaPlacement struct {
				Typename                  string `json:"__typename"`
				Revision, ParentRevision  string
				Complete, SourceEvaluated bool
				Candidates                []any
				Transitions               []any
				Selected                  struct {
					ClusterID          string
					NodeID             *string
					DistanceKm         *float64
					RequiresSourcePull bool
				}
			}
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		out := data.PreviewMediaPlacement
		if out.Typename != "MediaPlacementPreview" || out.Revision != "9007199254740993" || out.ParentRevision != "7" || !out.Complete || out.SourceEvaluated != sourceEvaluated || out.Selected.ClusterID != "us" || out.Selected.NodeID != nil || out.Selected.DistanceKm != nil || out.Selected.RequiresSourcePull != sourceEvaluated || out.Candidates == nil || out.Transitions == nil || fake.Calls != 1 {
			t.Fatalf("serialized preview lost semantics: %s", response.Data)
		}
	}
}
