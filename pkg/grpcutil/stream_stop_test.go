package grpcutil

import (
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestPartialStreamStopProgressSurvivesSanitization(t *testing.T) {
	progress := &controlpb.TerminateTenantStreamsResponse{StreamsTerminated: 1, SessionsTerminated: 2, StreamNames: []string{"owned"}}
	err := SanitizeError(PendingStreamStop(progress))
	got := StreamStopProgress(err)
	if status.Code(err) != codes.Unavailable || got.GetStreamsTerminated() != 1 || got.GetSessionsTerminated() != 2 || len(got.GetStreamNames()) != 1 {
		t.Fatalf("partial progress lost at service boundary: %v %v", got, err)
	}
}
