package handlers

import (
	"encoding/json"
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
		{name: "authorize complete", handler: h.AuthorizeComplete()},
		{name: "oauth token", handler: h.OAuthToken()},
		{name: "device start", handler: h.DeviceStart()},
		{name: "device poll", handler: h.DevicePoll()},
		{name: "device lookup", handler: h.DeviceLookup()},
		{name: "device approve", handler: h.DeviceApprove()},
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

// TestHandleBotCheckError locks the gRPC-to-HTTP mapping for Turnstile
// failures. The Commodore turnstile branch returns codes.PermissionDenied
// with message containing "bot verification"; the gateway must surface this
// as HTTP 403 + error_code=BOT_CHECK_FAILED so the webapp can render a
// distinct message instead of the generic "invalid credentials" 401.
// Any drift here regresses the user-visible bot-check experience and the
// tray's eventual debug story.
func TestHandleBotCheckError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		err         error
		wantHandled bool
		wantStatus  int
		wantCode    string
	}{
		{
			name:        "bot check failure produces 403 + BOT_CHECK_FAILED",
			err:         status.Error(codes.PermissionDenied, "bot verification failed"),
			wantHandled: true,
			wantStatus:  http.StatusForbidden,
			wantCode:    "BOT_CHECK_FAILED",
		},
		{
			name:        "other PermissionDenied passes through (not bot-check)",
			err:         status.Error(codes.PermissionDenied, "account suspended"),
			wantHandled: false,
		},
		{
			name:        "Unauthenticated passes through",
			err:         status.Error(codes.Unauthenticated, "invalid credentials"),
			wantHandled: false,
		},
		{
			name:        "plain error passes through",
			err:         errors.New("network sad"),
			wantHandled: false,
		},
		{
			name:        "nil passes through",
			err:         nil,
			wantHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			got := handleBotCheckError(c, tc.err)
			if got != tc.wantHandled {
				t.Fatalf("handleBotCheckError() = %v, want %v", got, tc.wantHandled)
			}
			if !tc.wantHandled {
				if rec.Code != http.StatusOK && rec.Code != 0 {
					t.Fatalf("handler should not have written a response; got status %d", rec.Code)
				}
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error_code"] != tc.wantCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], tc.wantCode)
			}
			if body["error"] == "" {
				t.Fatal("error message must be non-empty")
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
