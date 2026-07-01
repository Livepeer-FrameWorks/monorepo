package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

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
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(`{"broken_json":`))
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

func TestHandleEmailNotVerifiedLoginError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		message     string
		wantHandled bool
	}{
		{
			name:        "verified password but unverified email gets stable code",
			message:     "email not verified",
			wantHandled: true,
		},
		{
			name:        "alternate verification wording gets stable code",
			message:     "please verify your email before signing in",
			wantHandled: true,
		},
		{
			name:        "not activated wording gets stable code",
			message:     "account not activated",
			wantHandled: true,
		},
		{
			name:        "activate account wording gets stable code",
			message:     "please activate your account before signing in",
			wantHandled: true,
		},
		{
			name:        "invalid credentials remains generic",
			message:     "invalid credentials",
			wantHandled: false,
		},
		{
			name:        "deactivated account remains separate",
			message:     "account deactivated",
			wantHandled: false,
		},
		{
			name:        "empty message passes through",
			message:     "",
			wantHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			got := handleEmailNotVerifiedLoginError(c, tc.message)
			if got != tc.wantHandled {
				t.Fatalf("handleEmailNotVerifiedLoginError() = %v, want %v", got, tc.wantHandled)
			}
			if !tc.wantHandled {
				if rec.Code != http.StatusOK && rec.Code != 0 {
					t.Fatalf("handler should not have written a response; got status %d", rec.Code)
				}
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusForbidden)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error_code"] != emailNotVerifiedErrorCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], emailNotVerifiedErrorCode)
			}
			if body["error"] != "email not verified" {
				t.Fatalf("error: got %q, want %q", body["error"], "email not verified")
			}
		})
	}
}

func TestLoginMapsActivationErrorsToVerificationResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
		wantCode   string
	}{
		{
			name:       "email not verified",
			err:        status.Error(codes.Unauthenticated, "email not verified"),
			wantStatus: http.StatusForbidden,
			wantError:  "email not verified",
			wantCode:   emailNotVerifiedErrorCode,
		},
		{
			name:       "not activated",
			err:        status.Error(codes.Unauthenticated, "account not activated"),
			wantStatus: http.StatusForbidden,
			wantError:  "email not verified",
			wantCode:   emailNotVerifiedErrorCode,
		},
		{
			name:       "invalid credentials",
			err:        status.Error(codes.Unauthenticated, "invalid credentials"),
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid credentials",
		},
		{
			name:       "deactivated account is not verification",
			err:        status.Error(codes.Unauthenticated, "account deactivated"),
			wantStatus: http.StatusUnauthorized,
			wantError:  "account deactivated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandlers{
				commodore: &clientstest.FakeCommodore{
					LoginFn: func(_ context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
						if req.Email != "user@example.com" || req.Password != "correct-password" {
							t.Fatalf("login request = %+v", req)
						}
						return nil, tc.err
					},
				},
				logger: logging.NewLogger(),
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/auth/login",
				strings.NewReader(`{"email":"user@example.com","password":"correct-password"}`),
			)
			c.Request.Header.Set("Content-Type", "application/json")

			h.Login()(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error"] != tc.wantError {
				t.Fatalf("error: got %q, want %q", body["error"], tc.wantError)
			}
			if body["error_code"] != tc.wantCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], tc.wantCode)
			}
		})
	}
}

// TestRefreshToken_ErrorMapping locks the rule that only a definitive
// Unauthenticated from Commodore may clear the auth cookies. Clearing on
// transient errors (or on the losing side of a concurrent refresh) wipes the
// valid cookies a winning refresh just set and logs the user out everywhere.
func TestRefreshToken_ErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		err            error
		wantStatus     int
		wantClearedJar bool
	}{
		{
			name:           "unauthenticated clears cookies",
			err:            status.Error(codes.Unauthenticated, "invalid or expired refresh token"),
			wantStatus:     http.StatusUnauthorized,
			wantClearedJar: true,
		},
		{
			name:           "internal error keeps cookies",
			err:            status.Error(codes.Internal, "database error"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
		{
			name:           "unavailable keeps cookies",
			err:            status.Error(codes.Unavailable, "circuit breaker open"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
		{
			name:           "plain error keeps cookies",
			err:            errors.New("network sad"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandlers{
				commodore: &clientstest.FakeCommodore{
					RefreshTokenFn: func(_ context.Context, _ string) (*commodorepb.AuthResponse, error) {
						return nil, tc.err
					},
				},
				logger: logging.NewLogger(),
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/refresh", nil)
			c.Request.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: "stale-token"})

			h.RefreshToken()(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			cleared := false
			for _, cookie := range rec.Result().Cookies() {
				if cookie.Name == refreshTokenCookie && cookie.MaxAge < 0 {
					cleared = true
				}
			}
			if cleared != tc.wantClearedJar {
				t.Fatalf("refresh cookie cleared = %v, want %v", cleared, tc.wantClearedJar)
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
