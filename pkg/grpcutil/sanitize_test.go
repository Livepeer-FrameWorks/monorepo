package grpcutil

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSanitizeErrorPreservesClientActionableMessages(t *testing.T) {
	tests := []struct {
		name string
		code codes.Code
		msg  string
	}{
		{name: "invalid argument", code: codes.InvalidArgument, msg: "start_ms must be before stop_ms"},
		{name: "not found", code: codes.NotFound, msg: "stream not found"},
		{name: "already exists", code: codes.AlreadyExists, msg: "stream already exists with ingest_mode=pull"},
		{name: "failed precondition", code: codes.FailedPrecondition, msg: "clip source dispatch: no finalized chapter covers requested range"},
		{name: "resource exhausted", code: codes.ResourceExhausted, msg: "storage quota exceeded"},
		{name: "aborted", code: codes.Aborted, msg: "optimistic update conflict"},
		{name: "out of range", code: codes.OutOfRange, msg: "clip start is outside DVR window"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SanitizeError(status.Error(tc.code, tc.msg))
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("SanitizeError returned non-status error: %v", err)
			}
			if st.Code() != tc.code {
				t.Fatalf("SanitizeError code = %v, want %v", st.Code(), tc.code)
			}
			if st.Message() != tc.msg {
				t.Fatalf("SanitizeError message = %q, want %q", st.Message(), tc.msg)
			}
		})
	}
}

func TestSanitizeErrorPreservesNormalizedStatusMessage(t *testing.T) {
	inner := status.Error(codes.FailedPrecondition, "clip source dispatch: no source covers requested range")
	err := SanitizeError(fmt.Errorf("foghorn create clip: %w", inner))

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("SanitizeError returned non-status error: %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("SanitizeError code = %v, want %v", st.Code(), codes.FailedPrecondition)
	}
	want := "clip source dispatch: no source covers requested range"
	if st.Message() != want {
		t.Fatalf("SanitizeError message = %q, want %q", st.Message(), want)
	}
}

func TestSanitizeErrorScrubsSensitiveMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
		want string
	}{
		{name: "plain error", err: errors.New("dial tcp yuga-eu-1.internal:5433: password leaked"), code: codes.Internal, want: "internal error"},
		{name: "internal", err: status.Error(codes.Internal, "pq: relation private.table does not exist"), code: codes.Internal, want: "internal error"},
		{name: "unknown", err: status.Error(codes.Unknown, "panic: secret stack frame"), code: codes.Unknown, want: "internal error"},
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "jwt kid abc not accepted"), code: codes.Unauthenticated, want: "authentication required"},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "tenant ae0e... cannot access stream"), code: codes.PermissionDenied, want: "permission denied"},
		{name: "unavailable", err: status.Error(codes.Unavailable, "dial tcp yuga-eu-1.internal:5433: server misbehaving"), code: codes.Unavailable, want: "service temporarily unavailable"},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "query SELECT * timed out"), code: codes.DeadlineExceeded, want: "request timed out"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SanitizeError(tc.err)
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("SanitizeError returned non-status error: %v", err)
			}
			if st.Code() != tc.code {
				t.Fatalf("SanitizeError code = %v, want %v", st.Code(), tc.code)
			}
			if st.Message() != tc.want {
				t.Fatalf("SanitizeError message = %q, want %q", st.Message(), tc.want)
			}
		})
	}
}
