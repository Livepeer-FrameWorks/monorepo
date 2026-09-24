package triggers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestPlacementErrorCodeSeparatesDenialFromTransientFailure(t *testing.T) {
	_, unsupported := mist.IngestProtocol("dtsc")
	for _, tc := range []struct {
		name string
		err  error
		want ipcpb.IngestErrorCode
	}{
		{"policy_denied", status.Error(codes.PermissionDenied, "selected destination has no current placement permission"), ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		{"unsupported_protocol", unsupported, ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED},
		{"observation_unavailable", status.Error(codes.Unavailable, "selected destination observation is unavailable"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"incomplete_census", fmt.Errorf("%w: %w", balancer.ErrPlacementUnavailable, balancer.ErrPlacementObservationIncomplete), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"authority_race", status.Error(codes.FailedPrecondition, "placement authority facts changed during revalidation"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
		{"deadline", context.DeadlineExceeded, ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT},
		{"generic", errors.New("ingest placement admission is unavailable"), ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL},
	} {
		if got := ingestPlacementErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
