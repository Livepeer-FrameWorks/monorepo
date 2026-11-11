package grpcutil

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"time"
)

const BootstrapErrorDomain = "bootstrap.frameworks.network"
const BootstrapPublicIdentityPending = "BOOTSTRAP_PUBLIC_IDENTITY_PENDING"
const BootstrapControlCellChanged = "BOOTSTRAP_CONTROL_CELL_CHANGED"

// BootstrapPendingError carries only bounded public state. Its reason and delay
// survive server sanitization without forwarding downstream messages or metadata.
func BootstrapPendingError(reason string, retryAfter time.Duration) error {
	if reason != BootstrapPublicIdentityPending && reason != BootstrapControlCellChanged {
		return status.Error(codes.Unavailable, "edge bootstrap is pending")
	}
	retryAfter = max(time.Second, min(retryAfter, 5*time.Second))
	st, err := status.New(codes.Unavailable, "edge bootstrap is pending").WithDetails(
		&errdetails.ErrorInfo{Domain: BootstrapErrorDomain, Reason: reason},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)},
	)
	if err != nil {
		return status.Error(codes.Unavailable, "edge bootstrap is pending")
	}
	return st.Err()
}

func BootstrapState(err error) (reason string, retryAfter time.Duration, ok bool) {
	st, isStatus := statusFromError(err)
	if !isStatus || st.Code() != codes.Unavailable {
		return "", 0, false
	}
	retryAfter = 2 * time.Second
	for _, detail := range st.Details() {
		switch typed := detail.(type) {
		case *errdetails.ErrorInfo:
			if typed.GetDomain() == BootstrapErrorDomain && (typed.GetReason() == BootstrapPublicIdentityPending || typed.GetReason() == BootstrapControlCellChanged) {
				reason = typed.GetReason()
			}
		case *errdetails.RetryInfo:
			if delay := typed.GetRetryDelay(); delay != nil && delay.IsValid() {
				retryAfter = max(time.Second, min(delay.AsDuration(), 5*time.Second))
			}
		}
	}
	return reason, retryAfter, reason != ""
}
