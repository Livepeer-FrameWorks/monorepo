package grpcutil

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"testing"
	"time"
)

func TestSanitizeBootstrapStatePreservesOnlySafeReasonAndBoundedDelay(t *testing.T) {
	for _, reason := range []string{BootstrapPublicIdentityPending, BootstrapControlCellChanged} {
		downstream, err := status.New(codes.Unavailable, "private diagnostic payload").WithDetails(
			&errdetails.ErrorInfo{Domain: BootstrapErrorDomain, Reason: reason, Metadata: map[string]string{"private": "payload"}},
			&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Hour)},
			&errdetails.DebugInfo{Detail: "private diagnostic payload"},
		)
		if err != nil {
			t.Fatal(err)
		}
		sanitized := SanitizeError(downstream.Err())
		got, delay, ok := BootstrapState(sanitized)
		if !ok || got != reason || delay != 5*time.Second {
			t.Fatalf("bootstrap state lost: %v %v %v", got, delay, sanitized)
		}
		st := status.Convert(sanitized)
		if st.Message() != "edge bootstrap is pending" || len(st.Details()) != 2 {
			t.Fatalf("unsafe status survived: %v", st)
		}
		for _, detail := range st.Details() {
			if info, ok := detail.(*errdetails.ErrorInfo); ok && len(info.GetMetadata()) != 0 {
				t.Fatal("downstream metadata survived sanitization")
			}
		}
	}
}

func TestSanitizeBootstrapStateRejectsUnknownReasonDomainOrCode(t *testing.T) {
	for _, tc := range []struct {
		code           codes.Code
		domain, reason string
	}{
		{codes.Unavailable, "other.example", BootstrapPublicIdentityPending},
		{codes.Unavailable, BootstrapErrorDomain, "UNREVIEWED_REASON"},
		{codes.Internal, BootstrapErrorDomain, BootstrapPublicIdentityPending},
	} {
		st, err := status.New(tc.code, "private diagnostic").WithDetails(&errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason})
		if err != nil {
			t.Fatal(err)
		}
		sanitized := SanitizeError(st.Err())
		if _, _, ok := BootstrapState(sanitized); ok || len(status.Convert(sanitized).Details()) != 0 {
			t.Fatalf("untrusted bootstrap state survived: %v", sanitized)
		}
	}
}
