package loaders

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStreamIngestLoaderCachesPartialCellFailure(t *testing.T) {
	calls := 0
	client := &clientstest.FakeCommodore{GetStreamIngestStatesFn: func(_ context.Context, ids []string) (*pb.GetStreamIngestStatesResponse, error) {
		calls++
		if len(ids) != 2 {
			t.Fatalf("want one batch of two streams, got %v", ids)
		}
		return &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{"healthy": {State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true}}, UnavailableStreamIds: []string{"down"}}, nil
	}}
	loader := NewStreamIngestLoader(client)
	if err := loader.LoadMany(t.Context(), "tenant", []string{"healthy", "down", "healthy"}); err != nil {
		t.Fatal(err)
	}
	if got, err := loader.Load(t.Context(), "tenant", "healthy"); err != nil || !got.GetIsLive() {
		t.Fatalf("healthy=%v, %v", got, err)
	}
	for range 2 {
		if got, err := loader.Load(t.Context(), "tenant", "down"); got != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("down=%v, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("partial failures caused %d calls", calls)
	}
}
