package resolvers

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func previewAPIFixture() (model.PreviewMediaPlacementInput, *placementpb.PreviewRequest, *placementpb.Preview) {
	now := time.Now().UTC()
	in := model.PreviewMediaPlacementInput{Scope: &model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindStream, StreamID: strPtr("stream")}, Verb: model.MediaPlacementVerbServe, ExpectedRevision: strPtr("9007199254740993"), ExpectedParentRevision: strPtr("7"), Coordinates: &model.MediaPlacementCoordinatesInput{Latitude: 39, Longitude: -77}, DraftUpdate: &model.MediaPlacementVerbUpdateInput{Verb: model.MediaPlacementVerbServe, Kind: model.MediaPlacementUpdateKindClear}}
	req := &placementpb.PreviewRequest{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}, Verb: placementpb.Verb_VERB_SERVE, ExpectedRevision: proto.Uint64(9007199254740993), ExpectedParentRevision: proto.Uint64(7), Coordinates: &placementpb.Coordinates{Latitude: 39, Longitude: -77}, DraftUpdate: &placementpb.VerbUpdate{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}}
	row := &placementpb.PreviewCandidate{ClusterId: "us", ClusterName: "US", GroupId: "entitled", Reason: "selected", DistanceKm: proto.Float64(0)}
	out := &placementpb.Preview{Scope: req.Scope, Verb: req.Verb, Revision: req.GetExpectedRevision(), ParentRevision: req.GetExpectedParentRevision(), Digest: strings.Repeat("a", 64), Reason: "selected", Selected: row, Candidates: []*placementpb.PreviewCandidate{proto.CloneOf(row)}, Complete: true, ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(20 * time.Second))}
	return in, req, out
}

func TestPlacementAPIPreviewPreservesDraftAndReadDeadline(t *testing.T) {
	in, want, response := previewAPIFixture()
	fake := &clientstest.FakeCommodore{PreviewMediaPlacementFn: func(ctx context.Context, req *placementpb.PreviewRequest) (*placementpb.Preview, error) {
		if !proto.Equal(req, want) {
			t.Fatalf("preview intent changed: %+v", req)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 9*time.Second {
			t.Fatal("preview deadline missing")
		}
		return response, nil
	}}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	out, err := r.DoPreviewMediaPlacement(placementAPITestContext(), in)
	preview, ok := out.(*model.MediaPlacementPreview)
	if err != nil || !ok || preview.Revision != *in.ExpectedRevision || preview.ParentRevision != "7" || preview.SourceEvaluated || preview.Selected.NodeID != nil || preview.Selected.DistanceKm == nil || *preview.Selected.DistanceKm != 0 || len(preview.Transitions) != 0 {
		t.Fatalf("preview projection lost semantics: %+v %v", out, err)
	}
}

func TestPlacementAPIPreviewRejectsInvalidInputBeforeRPC(t *testing.T) {
	for _, scenario := range []string{"verb", "scope", "revision", "missing revision", "missing parent", "wrong verb", "foreign stream", "nan", "protocol"} {
		t.Run(scenario, func(t *testing.T) {
			in, _, _ := previewAPIFixture()
			switch scenario {
			case "verb":
				in.Verb = "STORAGE"
			case "scope":
				in.Scope = nil
			case "revision":
				in.ExpectedRevision = strPtr("01")
			case "missing revision":
				in.ExpectedRevision = nil
			case "missing parent":
				in.ExpectedParentRevision = nil
			case "wrong verb":
				in.DraftUpdate.Verb = model.MediaPlacementVerbIngest
			case "foreign stream":
				in.StreamID = strPtr("other")
			case "nan":
				in.Coordinates.Latitude = math.NaN()
			case "protocol":
				in.Protocol = strPtr("HLS")
			}
			fake := &clientstest.FakeCommodore{}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			out, err := r.DoPreviewMediaPlacement(placementAPITestContext(), in)
			detail, ok := out.(*model.MediaPlacementError)
			if err != nil || !ok || detail.Code != model.MediaPlacementErrorCodeInvalidInput || fake.Calls != 0 {
				t.Fatalf("invalid input reached RPC: %+v %v", out, err)
			}
		})
	}
}

func TestPlacementAPIPreviewRejectsInconsistentOutput(t *testing.T) {
	for _, scenario := range []string{"nil", "scope", "verb", "revision", "digest", "expired", "future", "long lease", "nan", "price", "invented source", "nil candidate", "nil transition", "serve pin"} {
		t.Run(scenario, func(t *testing.T) {
			_, req, response := previewAPIFixture()
			switch scenario {
			case "nil":
				response = nil
			case "scope":
				response.Scope = &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}
			case "verb":
				response.Verb = placementpb.Verb_VERB_INGEST
			case "revision":
				response.Revision++
			case "digest":
				response.Digest = "unknown"
			case "expired":
				response.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "future":
				response.ObservedAt = timestamppb.New(time.Now().Add(5 * time.Second))
			case "long lease":
				response.ExpiresAt = timestamppb.New(time.Now().Add(time.Minute))
			case "nan":
				response.Selected.DistanceKm = proto.Float64(math.NaN())
			case "price":
				response.Selected.Price = &placementpb.PreviewPrice{}
			case "invented source":
				response.Selected.RequiresSourcePull = true
			case "nil candidate":
				response.Candidates = append(response.Candidates, nil)
			case "nil transition":
				response.Transitions = []*placementpb.PreviewTransition{nil}
			case "serve pin":
				response.ActiveIngestClusterId = "us"
			}
			if _, err := placementPreviewOutput(req, response, time.Now()); err == nil {
				t.Fatal("inconsistent response accepted")
			}
		})
	}
}

func TestPlacementAPIPreviewErrorAndAuthBoundary(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Aborted, codes.InvalidArgument, codes.FailedPrecondition, codes.Unimplemented} {
		out := placementPreviewFailure(status.Error(code, "private source address"))
		detail, ok := out.(*model.MediaPlacementError)
		if !ok || strings.Contains(detail.Message, "private") || strings.Contains(detail.Message, "write") || detail.Code == model.MediaPlacementErrorCodeStaleReview {
			t.Fatalf("read error leaked write semantics: %+v", out)
		}
	}
	in, _, _ := previewAPIFixture()
	fake := &clientstest.FakeCommodore{}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
	for _, ctx := range []context.Context{context.Background(), context.WithValue(placementAPITestContext(), ctxkeys.KeyAuthType, "api_token")} {
		out, err := r.DoPreviewMediaPlacement(ctx, in)
		if _, ok := out.(*model.AuthError); !ok || err != nil || fake.Calls != 0 {
			t.Fatalf("unauthorized preview reached RPC: %+v %v", out, err)
		}
	}
}
