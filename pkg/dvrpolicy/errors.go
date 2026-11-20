package dvrpolicy

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ErrorInfoDomain              = "recording.frameworks.network"
	RecordingPolicyPendingReason = "RECORDING_POLICY_PENDING"
)

// RecordingPolicyPendingError refuses new work while the signed recording
// configuration is behind the owner's saved settings. FailedPrecondition is
// deliberate: a reachable owner with pending configuration is not an outage
// and must never enable offline admission using the superseded authority.
func RecordingPolicyPendingError() error {
	st := status.New(codes.FailedPrecondition, "Recording settings are still being applied. Please retry shortly.")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Domain: ErrorInfoDomain, Reason: RecordingPolicyPendingReason})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func IsRecordingPolicyPending(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return false
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == ErrorInfoDomain && info.GetReason() == RecordingPolicyPendingReason {
			return true
		}
	}
	return false
}
