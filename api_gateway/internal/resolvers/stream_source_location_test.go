package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUpdateStreamSendsRestrictedSourceLocation(t *testing.T) {
	var sent *commodorepb.StreamSourceLocation
	r := resolverWith(&clientstest.FakeCommodore{
		UpdateStreamFn: func(_ context.Context, req *commodorepb.UpdateStreamRequest) (*commodorepb.Stream, error) {
			sent = req.GetSourceLocation()
			return &commodorepb.Stream{StreamId: req.GetStreamId()}, nil
		},
	})
	input := model.UpdateStreamInput{SourceLocation: &model.SourceLocationInput{
		Mode:         model.SourceLocationModeRestricted,
		Clusters:     []*model.SourceLocationClusterInput{{ClusterID: " lan-cluster ", NodeIds: []string{"node-a", " "}}},
		AvoidNodeIds: []string{"node-b"},
	}}
	result, err := r.DoUpdateStream(clientstest.AuthedCtx("t1"), "stream-1", input)
	if _, ok := result.(*commodorepb.Stream); err != nil || !ok {
		t.Fatalf("result=%T err=%v", result, err)
	}
	if sent.GetMode() != commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED || len(sent.GetClusters()) != 1 ||
		sent.GetClusters()[0].GetClusterId() != "lan-cluster" || len(sent.GetClusters()[0].GetNodeIds()) != 1 ||
		len(sent.GetAvoidNodeIds()) != 1 {
		t.Fatalf("sent source location = %v", sent)
	}
}

func TestSourceLocationInputShapeErrorsNeverReachCommodore(t *testing.T) {
	r := resolverWith(&clientstest.FakeCommodore{})
	for name, input := range map[string]*model.SourceLocationInput{
		"custom":                {Mode: model.SourceLocationModeCustom},
		"restricted no cluster": {Mode: model.SourceLocationModeRestricted},
		"any with clusters":     {Mode: model.SourceLocationModeAny, Clusters: []*model.SourceLocationClusterInput{{ClusterID: "c"}}},
	} {
		result, err := r.DoUpdateStream(clientstest.AuthedCtx("t1"), "stream-1", model.UpdateStreamInput{SourceLocation: input})
		if _, ok := result.(*model.ValidationError); err != nil || !ok {
			t.Fatalf("%s: result=%T err=%v, want ValidationError", name, result, err)
		}
	}
}

// Commodore's private-source and entitlement refusals reach the caller as the
// actionable reason, not a transport error.
func TestCreateStreamSurfacesPlacementRefusalsAsValidationErrors(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition} {
		r := resolverWith(&clientstest.FakeCommodore{
			CreateStreamFn: func(context.Context, *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
				return nil, status.Error(code, "private or multicast pull sources require a source location restricted to clusters that allow private pull sources")
			},
		})
		result, err := r.DoCreateStream(clientstest.AuthedCtx("t1"), model.CreateStreamInput{Name: "cam"})
		vErr, ok := result.(*model.ValidationError)
		if err != nil || !ok || vErr.Message == "" {
			t.Fatalf("%v: result=%T err=%v", code, result, err)
		}
	}
}

func TestStreamSourceLocationNeverReportsAnyForAMissingLocation(t *testing.T) {
	r := resolverWith(&clientstest.FakeCommodore{})
	if _, err := r.DoStreamSourceLocation(clientstest.AuthedCtx("t1"), &commodorepb.Stream{StreamId: "s"}); err == nil {
		t.Fatal("stream without a source location reported one")
	}
	got, err := r.DoStreamSourceLocation(clientstest.AuthedCtx("t1"), &commodorepb.Stream{SourceLocation: &commodorepb.StreamSourceLocation{
		Mode:     commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED,
		Clusters: []*commodorepb.SourceLocationCluster{{ClusterId: "lan", NodeIds: []string{"n1"}}},
	}})
	if err != nil || got.Mode != model.SourceLocationModeRestricted || len(got.Clusters) != 1 || got.Clusters[0].NodeIds[0] != "n1" {
		t.Fatalf("restricted location = %+v, %v", got, err)
	}
}
