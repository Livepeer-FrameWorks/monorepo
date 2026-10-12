package control

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A preparation that ran out of its budget or whose attempt was abandoned is
// retried by the viewer like an unavailable dependency; a signed refusal is
// not.
func TestClassifyViewerPlacementErrorRetriesExhaustedAndAbortedPreparations(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ViewerPlacementErrorClass
	}{
		{"remote deadline", status.Error(codes.DeadlineExceeded, "context deadline exceeded"), ViewerPlacementNotReady},
		{"local budget", fmt.Errorf("prepare viewer: %w", context.DeadlineExceeded), ViewerPlacementNotReady},
		{"aborted attempt", status.Error(codes.Aborted, "placement attempt is expired"), ViewerPlacementNotReady},
		{"unavailable", status.Error(codes.Unavailable, "receipt storage"), ViewerPlacementNotReady},
		{"denied", status.Error(codes.PermissionDenied, "policy"), ViewerPlacementDenied},
		{"authority denied", balancer.ErrPlacementAuthorityDenied, ViewerPlacementDenied},
		{"viewer went away", fmt.Errorf("prepare viewer: %w", context.Canceled), ViewerPlacementErrorOther},
		{"internal", errors.New("boom"), ViewerPlacementErrorOther},
	} {
		if got := ClassifyViewerPlacementError(tc.err); got != tc.want {
			t.Errorf("%s: class %d, want %d", tc.name, got, tc.want)
		}
	}
}
