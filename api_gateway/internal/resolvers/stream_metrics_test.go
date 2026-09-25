package resolvers

import (
	"testing"
	"time"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestObservedStreamMetrics(t *testing.T) {
	// The exact response Periscope's GetStreamStatus returns for a stream with
	// no stream_state_current row (a freshly created stream).
	unobserved := &periscopepb.StreamStatusResponse{StreamId: "s", Status: "offline"}
	if got := ObservedStreamMetrics(unobserved); got != nil {
		t.Fatalf("an unobserved stream must resolve metrics to null, got %+v", got)
	}
	if got := ObservedStreamMetrics(nil); got != nil {
		t.Fatalf("a nil response must resolve metrics to null, got %+v", got)
	}
	observed := &periscopepb.StreamStatusResponse{StreamId: "s", Status: "live", UpdatedAt: timestamppb.New(time.Now())}
	if got := ObservedStreamMetrics(observed); got != observed {
		t.Fatal("an observed stream's metrics must be returned unchanged")
	}
}
