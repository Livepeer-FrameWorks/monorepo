package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIngestFrontDoorFailureNamesTheCause(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		code       string
		httpStatus int
	}{
		{"incomplete_census", fmt.Errorf("%w: %w", balancer.ErrPlacementUnavailable, balancer.ErrPlacementObservationIncomplete), "INGEST_PLACEMENT_UNAVAILABLE", http.StatusServiceUnavailable},
		{"no_nodes", balancer.ErrPlacementUnavailable, "NO_INGEST_NODES", http.StatusServiceUnavailable},
		{"denied", status.Error(codes.PermissionDenied, "no permission"), "INGEST_PLACEMENT_DENIED", http.StatusForbidden},
		{"deadline", context.DeadlineExceeded, "INGEST_PLACEMENT_UNAVAILABLE", http.StatusServiceUnavailable},
		{"other", errors.New("ingest preparation does not match the publisher request"), "INGEST_RESOLUTION_FAILED", http.StatusServiceUnavailable},
	} {
		code, httpStatus, _ := ingestFrontDoorFailure(tc.err)
		if code != tc.code || httpStatus != tc.httpStatus {
			t.Errorf("%s: got %s/%d, want %s/%d", tc.name, code, httpStatus, tc.code, tc.httpStatus)
		}
	}
}
