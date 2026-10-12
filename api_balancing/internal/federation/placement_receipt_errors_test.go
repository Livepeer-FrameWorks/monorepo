package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	redisserver "github.com/alicebob/miniredis/v2/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Receipt storage that cannot answer refuses this preparation only for now: the
// federation boundary and the viewer path must see a status they retry, not an
// internal failure.
func TestPlacementReceiptStorageRefusalIsRetryable(t *testing.T) {
	store, server, req := placementReceiptFixture(t)
	ctx := context.Background()
	placementReceiptEngineInfo(server, "a", "b", "0", "slave")
	_, err := store.Begin(ctx, req)
	if !errors.Is(err, ErrPlacementReceiptUnavailable) {
		t.Fatalf("replica storage = %v, want receipt storage unavailable", err)
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("receipt storage refusal carries %s, want Unavailable", status.Code(err))
	}
	if mapped := (&FederationServer{}).placementRPCError("prepare", "tenant", "internal", "us", "edge", err); status.Code(mapped) != codes.Unavailable {
		t.Fatalf("federation boundary turned receipt storage refusal into %s", status.Code(mapped))
	}
	if control.ClassifyViewerPlacementError(err) != control.ViewerPlacementNotReady {
		t.Fatal("viewer path does not treat receipt storage refusal as retryable")
	}
}

// An attempt that conflicts with its recorded intent, lost its record, or
// expired cannot complete, but a new attempt can: each refusal carries Aborted
// across the federation boundary, keeps its identity, and the viewer path
// retries it instead of reporting an internal failure.
func TestPlacementReceiptAttemptRefusalsAbortTheAttempt(t *testing.T) {
	for _, refusal := range []error{ErrPlacementReceiptConflict, ErrPlacementReceiptMissing, ErrPlacementReceiptExpired} {
		wrapped := fmt.Errorf("prepare: %w", refusal)
		if status.Code(refusal) != codes.Aborted {
			t.Errorf("%v carries %s, want Aborted", refusal, status.Code(refusal))
		}
		if !errors.Is(wrapped, refusal) {
			t.Errorf("%v lost its identity when wrapped", refusal)
		}
		mapped := (&FederationServer{}).placementRPCError("prepare", "tenant", "internal", "us", "edge", wrapped)
		if status.Code(mapped) != codes.Aborted {
			t.Errorf("federation boundary turned %v into %s", refusal, status.Code(mapped))
		}
		if control.ClassifyViewerPlacementError(mapped) != control.ViewerPlacementNotReady {
			t.Errorf("viewer path does not retry %v", refusal)
		}
	}
	for _, pair := range [][2]error{{ErrPlacementReceiptConflict, ErrPlacementReceiptMissing}, {ErrPlacementReceiptMissing, ErrPlacementReceiptExpired}, {ErrPlacementReceiptExpired, ErrPlacementReceiptConflict}} {
		if errors.Is(pair[0], pair[1]) {
			t.Errorf("%v matches %v", pair[0], pair[1])
		}
	}
}

// A receipt operation the caller's deadline cuts off reports that deadline, not
// a storage failure: under a viewer herd the preparation ran out of its budget
// while Redis was still answering.
func TestPlacementReceiptOperationCutByDeadlineReportsTheDeadline(t *testing.T) {
	store, server, req := placementReceiptFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Redis answers the script only after the caller's deadline has passed.
	server.Server().SetPreHook(func(peer *redisserver.Peer, command string, _ ...string) bool {
		if !strings.EqualFold(command, "EVALSHA") && !strings.EqualFold(command, "EVAL") {
			return false
		}
		<-ctx.Done()
		peer.WriteError("ERR answered after the deadline")
		return true
	})
	_, err := store.Begin(ctx, req)
	if errors.Is(err, ErrPlacementReceiptUnavailable) {
		t.Fatalf("deadline-cut receipt operation reported %v", err)
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("deadline-cut receipt operation = %v (%s), want DeadlineExceeded", err, status.Code(err))
	}
}
