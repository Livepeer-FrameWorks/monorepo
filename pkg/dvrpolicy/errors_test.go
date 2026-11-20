package dvrpolicy

import (
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecordingPolicyPendingSurvivesTransportAndWrapping(t *testing.T) {
	wire := status.Convert(RecordingPolicyPendingError()).Proto()
	err := fmt.Errorf("publisher lease: %w", status.FromProto(wire).Err())
	if !IsRecordingPolicyPending(err) || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("pending configuration lost its typed, non-outage status: %v", err)
	}
}

func TestRecordingPolicyPendingDoesNotMatchOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		code           codes.Code
		domain, reason string
	}{
		{codes.Unavailable, ErrorInfoDomain, RecordingPolicyPendingReason},
		{codes.FailedPrecondition, "other.frameworks.network", RecordingPolicyPendingReason},
		{codes.FailedPrecondition, ErrorInfoDomain, "OTHER"},
	} {
		st, err := status.New(tc.code, "untrusted text").WithDetails(&errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason})
		if err != nil {
			t.Fatal(err)
		}
		if IsRecordingPolicyPending(st.Err()) {
			t.Fatalf("wrong error identity matched: %v", tc)
		}
	}
	if IsRecordingPolicyPending(nil) || IsRecordingPolicyPending(status.Error(codes.FailedPrecondition, RecordingPolicyPendingReason)) {
		t.Fatal("missing ErrorInfo was accepted as a recording policy state")
	}
}
