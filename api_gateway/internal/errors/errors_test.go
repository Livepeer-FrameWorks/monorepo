package errors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrorPresenterMapsLocalAuthorizationErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{auth.ErrUnauthenticated, "UNAUTHORIZED"},
		{middleware.ErrForbidden, "FORBIDDEN"},
	} {
		presented := ErrorPresenter(logging.NewLogger())(context.Background(), fmt.Errorf("tenant events: %w", tc.err))
		if presented.Extensions["code"] != tc.code {
			t.Errorf("%v: got code %v, want %s", tc.err, presented.Extensions["code"], tc.code)
		}
	}
}

func TestErrorPresenterPreservesStructuredBillingBlocker(t *testing.T) {
	st := status.New(codes.FailedPrecondition, "add a billing email and postal address before paying")
	st, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: "BILLING_PROFILE_REQUIRED",
		Domain: "billing.frameworks.network",
		Metadata: map[string]string{
			"required_fields": "email, street,city,postal_code,country",
			"public_message":  "add a billing email and postal address before paying",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	presented := ErrorPresenter(logging.NewLogger())(context.Background(), fmt.Errorf("create top-up: %w", st.Err()))
	if presented.Message != "add a billing email and postal address before paying" {
		t.Fatalf("message = %q", presented.Message)
	}
	if presented.Extensions["code"] != "BILLING_PROFILE_REQUIRED" {
		t.Fatalf("code = %#v", presented.Extensions["code"])
	}
	fields, ok := presented.Extensions["required_fields"].([]string)
	if !ok || len(fields) != 5 || fields[0] != "email" || fields[4] != "country" {
		t.Fatalf("required_fields = %#v", presented.Extensions["required_fields"])
	}
}

func TestErrorPresenterMapsGRPCStatusToExtensionCode(t *testing.T) {
	cases := []struct {
		code    codes.Code
		want    string
		message string
	}{
		{codes.Unauthenticated, "UNAUTHORIZED", "authentication required"},
		{codes.PermissionDenied, "FORBIDDEN", "permission denied"},
		{codes.NotFound, "NOT_FOUND", "resource not found"},
		{codes.InvalidArgument, "VALIDATION_ERROR", "invalid request"},
		{codes.AlreadyExists, "CONFLICT", "resource already exists"},
		{codes.FailedPrecondition, "FAILED_PRECONDITION", "request not allowed in the current state"},
		{codes.ResourceExhausted, "RATE_LIMITED", "rate limit exceeded"},
		{codes.Unavailable, "UNAVAILABLE", "service temporarily unavailable"},
		{codes.DeadlineExceeded, "UNAVAILABLE", "request timed out"},
		{codes.Internal, "INTERNAL_ERROR", "internal error"},
		{codes.Unknown, "INTERNAL_ERROR", "internal error"},
	}
	presenter := ErrorPresenter(logging.NewLogger())
	for _, tc := range cases {
		t.Run(tc.code.String(), func(t *testing.T) {
			err := fmt.Errorf("resolver: %w", status.Error(tc.code, "backend detail that must not leak"))
			presented := presenter(context.Background(), err)
			if presented.Extensions["code"] != tc.want {
				t.Fatalf("extensions.code = %#v, want %q", presented.Extensions["code"], tc.want)
			}
			if presented.Message != tc.message {
				t.Fatalf("message = %q, want %q", presented.Message, tc.message)
			}
			if presented.Message == "backend detail that must not leak" {
				t.Fatal("presenter leaked the backend status message")
			}
		})
	}
}

func TestErrorPresenterLeavesPlainErrorsWithoutCode(t *testing.T) {
	presented := ErrorPresenter(logging.NewLogger())(context.Background(), errors.New("stream name is required"))
	if _, ok := presented.Extensions["code"]; ok {
		t.Fatalf("plain resolver error gained a code: %#v", presented.Extensions)
	}
}

func TestErrorPresenterKeepsExplicitCode(t *testing.T) {
	st := status.New(codes.FailedPrecondition, "billing")
	st, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: "BILLING_PROFILE_REQUIRED",
		Domain: "billing.frameworks.network",
	})
	if err != nil {
		t.Fatal(err)
	}
	presented := ErrorPresenter(logging.NewLogger())(context.Background(), st.Err())
	if presented.Extensions["code"] != "BILLING_PROFILE_REQUIRED" {
		t.Fatalf("code = %#v, want BILLING_PROFILE_REQUIRED", presented.Extensions["code"])
	}
}

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
		got := messageForCode(codes.DataLoss)
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
