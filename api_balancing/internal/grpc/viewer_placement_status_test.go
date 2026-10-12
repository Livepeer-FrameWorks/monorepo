package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A DVR viewer preparation that ran out of its budget, was aborted, or found
// no destination reaches the gateway as Unavailable, which it answers with a
// retryable 503; a policy refusal stays a refusal.
func TestViewerPlacementStatusRetriesExhaustedPreparation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{"local budget", fmt.Errorf("prepare viewer: %w", context.DeadlineExceeded), codes.Unavailable},
		{"remote deadline", status.Error(codes.DeadlineExceeded, "slow"), codes.Unavailable},
		{"aborted attempt", status.Error(codes.Aborted, "placement attempt is expired"), codes.Unavailable},
		{"no destination", balancer.ErrPlacementUnavailable, codes.Unavailable},
		{"denied", balancer.ErrPlacementAuthorityDenied, codes.PermissionDenied},
		{"invalid argument", status.Error(codes.InvalidArgument, "protocol"), codes.InvalidArgument},
		{"plain", errors.New("viewer preparation does not match the requested destination"), codes.Internal},
	} {
		if got := status.Code(viewerPlacementStatus(tc.err)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
