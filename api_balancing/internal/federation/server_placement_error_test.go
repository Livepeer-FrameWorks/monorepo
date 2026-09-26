package federation

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A runtime failure behind the placement RPC must reach the calling cell as a
// status it can act on, not the interceptor's anonymous internal error.
func TestPlacementRPCErrorMapsRuntimeFailures(t *testing.T) {
	s := &FederationServer{}
	if got := status.Code(s.placementRPCError("prepare", "t", "s", "c", "n", fmt.Errorf("arrange: %w", ErrOriginPullPeerUnreachable))); got != codes.Unavailable {
		t.Fatalf("unreachable origin-pull peer mapped to %s, want Unavailable", got)
	}
	refused := status.Error(codes.FailedPrecondition, "source has no retained placement demand")
	if got := s.placementRPCError("prepare", "t", "s", "c", "n", refused); !errors.Is(got, refused) {
		t.Fatalf("a status error must pass through unchanged, got %v", got)
	}
	if got := status.Code(s.placementRPCError("query", "t", "s", "", "", errors.New("boom"))); got != codes.Internal {
		t.Fatalf("an unclassified failure mapped to %s, want Internal", got)
	}
}
