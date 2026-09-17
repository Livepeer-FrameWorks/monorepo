package placement

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// ErrorInfoDomain is the google.rpc.ErrorInfo domain of placement refusals.
	ErrorInfoDomain = "placement.frameworks.network"
	// ReasonNodePlacementNotReady refuses node-bearing rules while any media
	// cell serving the tenant does not attest node placement.
	ReasonNodePlacementNotReady = "NODE_PLACEMENT_NOT_READY"
)

// NodePlacementNotReadyError is the FailedPrecondition status for
// ReasonNodePlacementNotReady. The ErrorInfo detail, not the code, is what
// distinguishes it from other failed preconditions such as a stale review.
func NodePlacementNotReadyError(message string) error {
	st := status.New(codes.FailedPrecondition, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Reason: ReasonNodePlacementNotReady, Domain: ErrorInfoDomain})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// IsNodePlacementNotReady reports whether err is a gRPC status carrying the
// ReasonNodePlacementNotReady ErrorInfo.
func IsNodePlacementNotReady(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return false
	}
	for _, detail := range st.Details() {
		if info, isInfo := detail.(*errdetails.ErrorInfo); isInfo && info.GetDomain() == ErrorInfoDomain && info.GetReason() == ReasonNodePlacementNotReady {
			return true
		}
	}
	return false
}
