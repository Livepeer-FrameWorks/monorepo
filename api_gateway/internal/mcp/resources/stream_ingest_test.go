package resources

import (
	"context"
	"encoding/json"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

func TestMCPStreamListUsesIngestState(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{Streams: []*commodorepb.Stream{{StreamId: "live", IsLive: false, Status: ""}}}, nil
		},
		GetStreamIngestStatesFn: func(context.Context, []string) (*pb.GetStreamIngestStatesResponse, error) {
			return &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{"live": {State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true}}}, nil
		},
	}
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, "tenant")
	result, err := handleStreamsList(ctx, &clients.ServiceClients{Commodore: fake}, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Streams []struct {
			IsLive bool   `json:"is_live"`
			Status string `json:"status"`
		}
	}
	if err := json.Unmarshal([]byte(result.Contents[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Streams) != 1 || !payload.Streams[0].IsLive || payload.Streams[0].Status != "live" {
		t.Fatalf("live stream is reported offline: %+v", payload)
	}
}
