package control

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCategorizeEnrollmentError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "x"), want: true},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "x"), want: true},
		{name: "invalid argument", err: status.Error(codes.InvalidArgument, "x"), want: true},
		{name: "internal", err: status.Error(codes.Internal, "x"), want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := categorizeEnrollmentError(tc.err); got != tc.want {
				t.Fatalf("categorizeEnrollmentError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestStreamCtx(t *testing.T) {
	ctx := streamCtx()
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("expected active context, got error: %v", err)
	}
}
