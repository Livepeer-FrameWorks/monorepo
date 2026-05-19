package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthHandlers_InvalidJSONBindingsReturnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &AuthHandlers{
		logger: logging.NewLogger(),
	}

	tests := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "login", handler: h.Login()},
		{name: "wallet login", handler: h.WalletLogin()},
		{name: "register", handler: h.Register()},
		{name: "resend verification", handler: h.ResendVerification()},
		{name: "forgot password", handler: h.ForgotPassword()},
		{name: "reset password", handler: h.ResetPassword()},
		{name: "update me", handler: h.UpdateMe()},
		{name: "update newsletter", handler: h.UpdateNewsletter()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"broken_json":`))
			c.Request.Header.Set("Content-Type", "application/json")

			tc.handler(c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if !strings.Contains(rec.Body.String(), "invalid request") {
				t.Fatalf("body: expected invalid request error, got %q", rec.Body.String())
			}
		})
	}
}

func TestIsAuthServiceUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("network sad"), want: false},
		{name: "unavailable", err: status.Error(codes.Unavailable, "circuit breaker open"), want: true},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "timeout"), want: true},
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "invalid credentials"), want: false},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "nope"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthServiceUnavailable(tc.err); got != tc.want {
				t.Fatalf("isAuthServiceUnavailable() = %v, want %v", got, tc.want)
			}
		})
	}
}
