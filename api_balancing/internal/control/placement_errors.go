package control

import (
	"context"
	"errors"

	"frameworks/api_balancing/internal/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ViewerPlacementErrorClass separates a placement refusal a viewer must not
// retry from a missing input that resolves on retry.
type ViewerPlacementErrorClass int

const (
	ViewerPlacementErrorOther ViewerPlacementErrorClass = iota
	// ViewerPlacementDenied is a signed or policy refusal.
	ViewerPlacementDenied
	// ViewerPlacementNotReady is authority that has not arrived, a dependency
	// that is unavailable right now, a preparation that ran out of its budget,
	// or an aborted preparation attempt that a new attempt can complete.
	ViewerPlacementNotReady
)

// ViewerPlacementRetryAfterSeconds is the retry advice for
// ViewerPlacementNotReady: long enough for a fetched authority or a recovered
// observation to land, short enough for a viewer start.
const ViewerPlacementRetryAfterSeconds = 2

func ClassifyViewerPlacementError(err error) ViewerPlacementErrorClass {
	switch {
	case err == nil:
		return ViewerPlacementErrorOther
	case errors.Is(err, balancer.ErrPlacementAuthorityDenied) || status.Code(err) == codes.PermissionDenied:
		return ViewerPlacementDenied
	case errors.Is(err, balancer.ErrPlacementAuthorityNotReady) || errors.Is(err, context.DeadlineExceeded):
		return ViewerPlacementNotReady
	case status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Aborted:
		return ViewerPlacementNotReady
	default:
		return ViewerPlacementErrorOther
	}
}
