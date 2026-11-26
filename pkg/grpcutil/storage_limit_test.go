package grpcutil

import (
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStorageLimitSurvivesForwarding(t *testing.T) {
	u, a, l, ok := StorageLimitState(SanitizeError(fmt.Errorf("forward: %w", StorageLimitError(11, 2, 10))))
	if !ok || u != 11 || a != 2 || l != 10 {
		t.Fatalf("forwarded quota=%d/%d/%d %v", u, a, l, ok)
	}
}

func TestStorageLimitRejectsOtherAndMalformedRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, domain, used, additional, limit string
		code                                  codes.Code
	}{
		{"other domain", "other", "11", "0", "10", codes.ResourceExhausted},
		{"other code", StorageErrorDomain, "11", "0", "10", codes.Internal},
		{"negative use", StorageErrorDomain, "-1", "0", "10", codes.ResourceExhausted},
		{"bad number", StorageErrorDomain, "eleven", "0", "10", codes.ResourceExhausted},
		{"no limit", StorageErrorDomain, "11", "0", "0", codes.ResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := status.New(tc.code, "private detail").WithDetails(&errdetails.ErrorInfo{Domain: tc.domain, Reason: StorageLimitReachedReason, Metadata: map[string]string{"used_bytes": tc.used, "additional_bytes": tc.additional, "limit_bytes": tc.limit}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, ok := StorageLimitState(st.Err()); ok {
				t.Fatal("nonquota error accepted as storage quota")
			}
		})
	}
}
