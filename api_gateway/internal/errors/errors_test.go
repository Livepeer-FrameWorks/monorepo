package errors

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSanitizeMessage(t *testing.T) {
	cases := []struct {
		name     string
		message  string
		fallback string
		allowed  []string
		want     string
	}{
		{
			name:     "empty message uses fallback",
			message:  "",
			fallback: "safe fallback",
			allowed:  []string{"safe"},
			want:     "safe fallback",
		},
		{
			name:     "whitespace message uses fallback",
			message:  "   ",
			fallback: "safe fallback",
			allowed:  []string{"safe"},
			want:     "safe fallback",
		},
		{
			name:     "allowed substring preserves trimmed",
			message:  "  Permission denied  ",
			fallback: "safe fallback",
			allowed:  []string{"denied"},
			want:     "Permission denied",
		},
		{
			name:     "allowed case-insensitive match",
			message:  "Invalid Request",
			fallback: "safe fallback",
			allowed:  []string{"request"},
			want:     "Invalid Request",
		},
		{
			name:     "allowed entries ignore empty strings",
			message:  "Missing data",
			fallback: "safe fallback",
			allowed:  []string{"", "missing"},
			want:     "Missing data",
		},
		{
			name:     "no allowed match uses fallback",
			message:  "Sensitive detail",
			fallback: "safe fallback",
			allowed:  []string{"safe"},
			want:     "safe fallback",
		},
		{
			name:     "fallback uses default when empty",
			message:  "",
			fallback: "",
			allowed:  []string{"safe"},
			want:     defaultPublicMessage,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeMessage(tc.message, tc.fallback, tc.allowed)
			if got != tc.want {
				t.Fatalf("SanitizeMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeGRPCError(t *testing.T) {
	allowed := []string{"safe", "denied"}
	cases := []struct {
		name     string
		err      error
		fallback string
		allowed  []string
		want     string
	}{
		{
			name:     "nil error uses fallback",
			err:      nil,
			fallback: "safe fallback",
			allowed:  allowed,
			want:     "safe fallback",
		},
		{
			name:     "status error allowed message",
			err:      status.Error(codes.InvalidArgument, "safe detail"),
			fallback: "safe fallback",
			allowed:  allowed,
			want:     "safe detail",
		},
		{
			name:     "status error disallowed message",
			err:      status.Error(codes.InvalidArgument, "private detail"),
			fallback: "safe fallback",
			allowed:  allowed,
			want:     "safe fallback",
		},
		{
			name:     "non-status error uses fallback",
			err:      errors.New("boom"),
			fallback: "safe fallback",
			allowed:  allowed,
			want:     "safe fallback",
		},
		{
			name:     "empty fallback uses default",
			err:      nil,
			fallback: "",
			allowed:  allowed,
			want:     defaultPublicMessage,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeGRPCError(tc.err, tc.fallback, tc.allowed)
			if got != tc.want {
				t.Fatalf("SanitizeGRPCError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeErrorMessage(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		fallback string
		want     string
	}{
		{
			name:     "nil error returns fallback",
			err:      nil,
			fallback: "safe fallback",
			want:     "safe fallback",
		},
		{
			name:     "status error maps to grpc message",
			err:      status.Error(codes.NotFound, "internal detail"),
			fallback: "safe fallback",
			want:     grpcCodeMessages[codes.NotFound],
		},
		{
			name:     "rpc error prefix returns fallback",
			err:      errors.New("rpc error: code = Unknown desc = unsafe"),
			fallback: "safe fallback",
			want:     "safe fallback",
		},
		{
			name:     "rpc error prefix uses default when fallback empty",
			err:      errors.New("rpc error: code = Unknown desc = unsafe"),
			fallback: "",
			want:     defaultPublicMessage,
		},
		{
			name:     "non-rpc error returns raw message",
			err:      errors.New("something happened"),
			fallback: "safe fallback",
			want:     "something happened",
		},
		{
			name:     "case-sensitive prefix does not match",
			err:      errors.New("RPC error: code = Unknown"),
			fallback: "safe fallback",
			want:     "RPC error: code = Unknown",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeErrorMessage(tc.err, tc.fallback)
			if got != tc.want {
				t.Fatalf("SanitizeErrorMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageForCode(t *testing.T) {
	for code, message := range grpcCodeMessages {
		code := code
		message := message
		t.Run(fmt.Sprintf("code_%s", code.String()), func(t *testing.T) {
			got := messageForCode(code)
			if got != message {
				t.Fatalf("messageForCode(%v) = %q, want %q", code, got, message)
			}
		})
	}

	t.Run("unknown code uses internal", func(t *testing.T) {
		got := messageForCode(codes.ResourceExhausted)
		want := grpcCodeMessages[codes.Internal]
		if got != want {
			t.Fatalf("messageForCode() = %q, want %q", got, want)
		}
	})
}

func TestFallbackMessage(t *testing.T) {
	cases := []struct {
		name     string
		fallback string
		want     string
	}{
		{
			name:     "empty fallback uses default",
			fallback: "",
			want:     defaultPublicMessage,
		},
		{
			name:     "non-empty fallback returns fallback",
			fallback: "safe fallback",
			want:     "safe fallback",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := fallbackMessage(tc.fallback)
			if got != tc.want {
				t.Fatalf("fallbackMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
